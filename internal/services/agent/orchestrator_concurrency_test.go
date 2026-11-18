package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// orgWithConcurrencyQuota returns an org store whose settings pin
// max_concurrent_runs, so tests can diverge the org quota from the
// process-wide fallback that buildOrchestrator configures (3).
func orgWithConcurrencyQuota(t *testing.T, orgID uuid.UUID, quota int) *mockOrgStore {
	t.Helper()
	settings, err := json.Marshal(map[string]any{"max_concurrent_runs": quota})
	require.NoError(t, err, "should encode org settings")
	return &mockOrgStore{org: models.Organization{ID: orgID, Settings: settings}}
}

func TestRunAgent_OrgConcurrencyQuota(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		orgs           func(orgID uuid.UUID) *mockOrgStore
		countExecuting int
		// holdsSlot re-enters RunAgent for a session whose previous attempt
		// reached BeginRuntime (running with deadlines installed), as worker
		// recovery does.
		holdsSlot    bool
		wantHeld     bool
		wantLimitMsg string
	}{
		{
			name:           "org quota above the process fallback admits the run",
			orgs:           func(orgID uuid.UUID) *mockOrgStore { return orgWithConcurrencyQuota(t, orgID, 5) },
			countExecuting: 3, // At the process fallback of 3, under the org quota of 5.
		},
		{
			name:           "org quota below the process fallback holds the run",
			orgs:           func(orgID uuid.UUID) *mockOrgStore { return orgWithConcurrencyQuota(t, orgID, 1) },
			countExecuting: 1, // Under the process fallback of 3, at the org quota of 1.
			wantHeld:       true,
			wantLimitMsg:   "1/1 runs active",
		},
		{
			name:           "unreadable org settings fall back to the configured limit",
			orgs:           func(uuid.UUID) *mockOrgStore { return &mockOrgStore{err: errors.New("org store unavailable")} },
			countExecuting: 3, // At the process fallback of 3.
			wantHeld:       true,
			wantLimitMsg:   "3/3 runs active",
		},
		{
			name:           "a recovering run that already holds its slot is admitted past a saturated quota",
			orgs:           func(orgID uuid.UUID) *mockOrgStore { return orgWithConcurrencyQuota(t, orgID, 1) },
			countExecuting: 1, // A sibling holds the only other slot; this run's own slot is not counted.
			holdsSlot:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := testOrg()
			issue := testIssue(orgID)
			run := testRun(orgID, issue.ID)
			if tt.holdsSlot {
				run.Status = models.SessionStatusRunning
				deadline := time.Now().Add(time.Hour)
				run.RuntimeHardDeadlineAt = &deadline
			}

			d := defaultDeps()
			d.orgs = tt.orgs(orgID)
			d.sessions.countExecuting = tt.countExecuting

			err := buildOrchestrator(d).RunAgent(context.Background(), run)
			if tt.holdsSlot {
				require.Empty(t, d.sessions.getCountExecutingExcludes(), "a run that already holds a slot is admitted without counting siblings")
			} else {
				require.Equal(t, []uuid.UUID{run.ID}, d.sessions.getCountExecutingExcludes(), "a run counts every executing session but itself")
			}
			if !tt.wantHeld {
				require.NoError(t, err, "RunAgent should admit the run under the org quota")
				require.Contains(t, d.sessions.getStatusUpdates(), "running", "an admitted run should be marked running")
				return
			}
			require.ErrorIs(t, err, agent.ErrConcurrencyLimit, "RunAgent should hold the run at the applied quota")
			require.Contains(t, err.Error(), tt.wantLimitMsg, "the concurrency error should report the quota that was applied")
			for _, status := range d.sessions.getStatusUpdates() {
				require.NotEqual(t, "running", status, "a quota-held run must not be marked running")
			}
			require.Equal(t, 0, d.provider.GetDestroyCalls(), "a quota-held run must not allocate a sandbox")
		})
	}
}

// claimedContinuation returns a session the API has already claimed for a
// follow-up (status running, as ClaimIdle leaves it) together with the queued
// user message its continue_session job will pick up. Each call gets its own
// session ID (testRun's is fixed) so two claimed sessions are distinct
// siblings to the executing count.
func claimedContinuation(orgID uuid.UUID, threadID *uuid.UUID) (*models.Session, models.SessionMessage) {
	issue := testIssue(orgID)
	session := testRun(orgID, issue.ID)
	session.ID = uuid.New()
	session.Status = models.SessionStatusRunning
	session.CurrentTurn = 1
	session.SnapshotKey = nil
	message := models.SessionMessage{
		ID:         1,
		SessionID:  session.ID,
		OrgID:      orgID,
		ThreadID:   threadID,
		TurnNumber: 2,
		Role:       models.MessageRoleUser,
		Content:    "Please continue.",
	}
	return session, message
}

func TestContinueSession_OrgConcurrencyQuota(t *testing.T) {
	t.Parallel()

	// The quota counts executing turns, not running sessions, and leaves the
	// continuing session itself out of the count. ClaimIdle/ClaimForResume
	// mark a session running when its message is accepted, so a running
	// count would hold queued continuations against each other.
	tests := []struct {
		name            string
		countExecuting  int
		executingIsSelf bool
		// holdsSlot continues a session whose row already carries the
		// executing-turn marker (running with deadlines installed): a thread
		// turn on a live session, or a recovery re-entry after a worker loss.
		holdsSlot    bool
		containerID  *string
		threadScoped bool
		// origin overrides the session origin; a code-review session is
		// counted by the quota but never held by it.
		origin      models.SessionOrigin
		wantHeld    bool
		wantCreated bool
	}{
		{
			name:           "cold continuation with no executing turn is admitted at quota 1",
			countExecuting: 0,
			wantCreated:    true,
		},
		{
			name:           "code-review session is admitted past a saturated quota without a count",
			countExecuting: 1,
			origin:         models.SessionOriginCodeReview,
		},
		{
			name:           "continuation with one executing sibling at quota 1 is held",
			countExecuting: 1,
			wantHeld:       true,
		},
		{
			name:           "warm reuse with one executing sibling at quota 1 is held too",
			countExecuting: 1,
			containerID:    strPtr("warm-container"),
			wantHeld:       true,
		},
		{
			name:            "thread turn on the executing session is not held by its own slot",
			countExecuting:  1,
			executingIsSelf: true,
			threadScoped:    true,
			wantCreated:     true,
		},
		{
			name:           "a session that already holds its slot is admitted past a saturated quota without a count",
			countExecuting: 1, // A sibling holds the only other slot; this session's own slot is not counted.
			holdsSlot:      true,
			wantCreated:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := testOrg()
			var threadID *uuid.UUID
			if tt.threadScoped {
				id := uuid.New()
				threadID = &id
			}
			session, message := claimedContinuation(orgID, threadID)
			session.ContainerID = tt.containerID
			if tt.containerID != nil {
				session.SandboxState = models.SandboxStateRunning
			}
			if tt.holdsSlot {
				deadline := time.Now().Add(time.Hour)
				session.RuntimeHardDeadlineAt = &deadline
			}
			if tt.origin != "" {
				session.Origin = tt.origin
			}

			d := defaultDeps()
			d.orgs = orgWithConcurrencyQuota(t, orgID, 1)
			d.sessions.countExecuting = tt.countExecuting
			if tt.executingIsSelf {
				d.sessions.executingSessionID = session.ID
			}
			d.messages.messages = []models.SessionMessage{message}
			created := false
			d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
				created = true
				return &agent.Sandbox{ID: "cold-sandbox", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
			}
			var opts *agent.ContinueSessionOptions
			if tt.threadScoped {
				opts = &agent.ContinueSessionOptions{ThreadID: threadID, AgentType: models.AgentTypeClaudeCode}
			}

			err := buildOrchestrator(d).ContinueSession(context.Background(), session, opts)
			switch {
			case tt.holdsSlot:
				require.Empty(t, d.sessions.getCountExecutingExcludes(), "a session that already holds a slot is admitted without counting siblings")
			case tt.origin == models.SessionOriginCodeReview:
				require.Empty(t, d.sessions.getCountExecutingExcludes(), "a code-review session is never held by the org quota, so the count is not consulted")
			default:
				require.Equal(t, []uuid.UUID{session.ID}, d.sessions.getCountExecutingExcludes(), "a continuation excludes its own session from the executing count")
			}
			if !tt.wantHeld {
				require.NotErrorIs(t, err, agent.ErrConcurrencyLimit, "the continuation must be admitted")
				if tt.origin == "" {
					require.Equal(t, tt.wantCreated, created, "sandbox allocation should match the continuation path")
				}
				return
			}
			require.ErrorIs(t, err, agent.ErrConcurrencyLimit, "the continuation must be held at the org quota")
			require.False(t, created, "a held continuation must not allocate a sandbox")
			for _, status := range d.sessions.getStatusUpdates() {
				require.NotEqual(t, "running", status, "a held continuation must not begin its runtime")
			}
			require.Equal(t, 0, d.provider.GetDestroyCalls(), "a held continuation must leave the recorded sandbox alone")
		})
	}
}

// TestContinueSession_QueuedContinuationsDoNotHoldEachOther proves the
// scenario the quota used to deadlock on: two sessions the API has claimed
// for follow-ups (both running) at quota 1 with no executing turn. The mock
// tracks the marker's lifecycle (BeginRuntime installs it, turn completion
// removes it), so the first turn occupies the only slot while it runs and
// releases it when it completes; the second continuation is then admitted
// instead of both waiting on each other until the worker's retry window
// expires.
func TestContinueSession_QueuedContinuationsDoNotHoldEachOther(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	first, firstMessage := claimedContinuation(orgID, nil)
	second, secondMessage := claimedContinuation(orgID, nil)

	d := defaultDeps()
	d.orgs = orgWithConcurrencyQuota(t, orgID, 1)
	d.sessions.trackExecuting = true // Both sessions are claimed, neither has begun a turn.
	d.messages.messages = []models.SessionMessage{firstMessage, secondMessage}
	created := 0
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		created++
		return &agent.Sandbox{ID: "cold-sandbox", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}
	orch := buildOrchestrator(d)

	require.NoError(t, orch.ContinueSession(context.Background(), first, nil), "the first queued continuation should run")
	require.Len(t, d.sessions.getTurnUpdates(), 1, "the first turn should complete and release its slot")
	require.NoError(t, orch.ContinueSession(context.Background(), second, nil), "the second queued continuation should run after the first")
	require.Equal(t, 2, created, "both continuations should have allocated a sandbox")
	require.Equal(t, []uuid.UUID{first.ID, second.ID}, d.sessions.getCountExecutingExcludes(), "each continuation should have been gated once, excluding only itself")
}

// TestContinueSession_HeldWhileSiblingTurnExecutes is the counterpart: with
// the first turn still executing (BeginRuntime ran, no completion yet), a
// second session's continuation at quota 1 is held.
func TestContinueSession_HeldWhileSiblingTurnExecutes(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	first, _ := claimedContinuation(orgID, nil)
	second, secondMessage := claimedContinuation(orgID, nil)

	d := defaultDeps()
	d.orgs = orgWithConcurrencyQuota(t, orgID, 1)
	d.sessions.trackExecuting = true
	d.sessions.executing = map[uuid.UUID]bool{first.ID: true} // The first turn is mid-execution.
	d.messages.messages = []models.SessionMessage{secondMessage}
	created := false
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		created = true
		return &agent.Sandbox{ID: "cold-sandbox", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}

	err := buildOrchestrator(d).ContinueSession(context.Background(), second, nil)
	require.ErrorIs(t, err, agent.ErrConcurrencyLimit, "the second continuation must wait for the executing turn")
	require.False(t, created, "a held continuation must not allocate a sandbox")
	require.Equal(t, []uuid.UUID{second.ID}, d.sessions.getCountExecutingExcludes(), "the held continuation excluded only itself")
}
