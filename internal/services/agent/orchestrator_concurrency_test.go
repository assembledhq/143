package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

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
		wantHeld       bool
		wantLimitMsg   string
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := testOrg()
			issue := testIssue(orgID)
			run := testRun(orgID, issue.ID)

			d := defaultDeps()
			d.orgs = tt.orgs(orgID)
			d.sessions.countExecuting = tt.countExecuting

			err := buildOrchestrator(d).RunAgent(context.Background(), run)
			require.Equal(t, []uuid.UUID{uuid.Nil}, d.sessions.getCountExecutingExcludes(), "a pending run counts every executing session")
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
// user message its continue_session job will pick up.
func claimedContinuation(orgID uuid.UUID, threadID *uuid.UUID) (*models.Session, models.SessionMessage) {
	issue := testIssue(orgID)
	session := testRun(orgID, issue.ID)
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
		containerID     *string
		threadScoped    bool
		wantHeld        bool
		wantCreated     bool
	}{
		{
			name:           "cold continuation with no executing turn is admitted at quota 1",
			countExecuting: 0,
			wantCreated:    true,
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
			require.Equal(t, []uuid.UUID{session.ID}, d.sessions.getCountExecutingExcludes(), "a continuation excludes its own session from the executing count")
			if !tt.wantHeld {
				require.NotErrorIs(t, err, agent.ErrConcurrencyLimit, "the continuation must be admitted")
				require.Equal(t, tt.wantCreated, created, "sandbox allocation should match the continuation path")
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
// for follow-ups (both running) at quota 1 with no executing turn. Neither
// claim counts as executing, so each continue_session job proceeds instead
// of both waiting on the other until the worker's retry window expires.
func TestContinueSession_QueuedContinuationsDoNotHoldEachOther(t *testing.T) {
	t.Parallel()

	orgID := testOrg()
	first, firstMessage := claimedContinuation(orgID, nil)
	second, secondMessage := claimedContinuation(orgID, nil)

	d := defaultDeps()
	d.orgs = orgWithConcurrencyQuota(t, orgID, 1)
	d.sessions.countExecuting = 0 // Both sessions are claimed, neither has begun a turn.
	d.messages.messages = []models.SessionMessage{firstMessage, secondMessage}
	created := 0
	d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
		created++
		return &agent.Sandbox{ID: "cold-sandbox", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
	}
	orch := buildOrchestrator(d)

	require.NoError(t, orch.ContinueSession(context.Background(), first, nil), "the first queued continuation should run")
	require.NoError(t, orch.ContinueSession(context.Background(), second, nil), "the second queued continuation should run after the first")
	require.Equal(t, 2, created, "both continuations should have allocated a sandbox")
	require.Equal(t, []uuid.UUID{first.ID, second.ID}, d.sessions.getCountExecutingExcludes(), "each continuation should have been gated once, excluding only itself")
}
