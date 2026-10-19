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
		name         string
		orgs         func(orgID uuid.UUID) *mockOrgStore
		countRunning int
		wantHeld     bool
		wantLimitMsg string
	}{
		{
			name:         "org quota above the process fallback admits the run",
			orgs:         func(orgID uuid.UUID) *mockOrgStore { return orgWithConcurrencyQuota(t, orgID, 5) },
			countRunning: 3, // At the process fallback of 3, under the org quota of 5.
		},
		{
			name:         "org quota below the process fallback holds the run",
			orgs:         func(orgID uuid.UUID) *mockOrgStore { return orgWithConcurrencyQuota(t, orgID, 1) },
			countRunning: 1, // Under the process fallback of 3, at the org quota of 1.
			wantHeld:     true,
			wantLimitMsg: "1/1 runs active",
		},
		{
			name:         "unreadable org settings fall back to the configured limit",
			orgs:         func(uuid.UUID) *mockOrgStore { return &mockOrgStore{err: errors.New("org store unavailable")} },
			countRunning: 3, // At the process fallback of 3.
			wantHeld:     true,
			wantLimitMsg: "3/3 runs active",
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
			d.sessions.countRunning = tt.countRunning

			err := buildOrchestrator(d).RunAgent(context.Background(), run)
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

func TestContinueSession_IsNotHeldAtOrgConcurrencyQuota(t *testing.T) {
	t.Parallel()

	// ClaimIdle/ClaimForResume mark a session running before its
	// continue_session job runs, so a quota gate on CountRunningByOrg would
	// count sibling queued continuations and deadlock them against each other.
	// Continuations must therefore proceed past the org quota on every path.
	tests := []struct {
		name        string
		containerID *string
		wantCreated bool
	}{
		{
			name:        "cold resume allocates a sandbox with the org quota saturated",
			wantCreated: true,
		},
		{
			name:        "warm resume proceeds with the org quota saturated",
			containerID: strPtr("warm-container"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := testOrg()
			issue := testIssue(orgID)
			session := testRun(orgID, issue.ID)
			// Claimed by the API before enqueue: already counted as running.
			session.Status = models.SessionStatusRunning
			session.CurrentTurn = 1
			session.SnapshotKey = nil
			session.ContainerID = tt.containerID
			if tt.containerID != nil {
				session.SandboxState = models.SandboxStateRunning
			}

			d := defaultDeps()
			d.orgs = orgWithConcurrencyQuota(t, orgID, 1)
			d.sessions.countRunning = 2 // This session plus one sibling claimed continuation.
			d.messages.messages = []models.SessionMessage{{
				ID:         1,
				SessionID:  session.ID,
				OrgID:      orgID,
				TurnNumber: 2,
				Role:       models.MessageRoleUser,
				Content:    "Please continue.",
			}}
			created := false
			d.provider.CreateFn = func(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
				created = true
				return &agent.Sandbox{ID: "cold-sandbox", Provider: "mock", WorkDir: cfg.WorkDir, HomeDir: cfg.HomeDir}, nil
			}

			err := buildOrchestrator(d).ContinueSession(context.Background(), session, nil)
			require.NotErrorIs(t, err, agent.ErrConcurrencyLimit, "a continuation must not be held at the org quota")
			require.Equal(t, tt.wantCreated, created, "sandbox allocation should match the continuation path")
		})
	}
}
