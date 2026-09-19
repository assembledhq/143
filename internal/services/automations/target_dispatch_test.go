package automations

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestTargetDispatcher_Applies(t *testing.T) {
	t.Parallel()

	targetID := uuid.New()
	tests := []struct {
		name string
		in   DispatchInput
		want bool
	}{
		{
			name: "per_target run attached to a target",
			in: DispatchInput{
				Run:        models.AutomationRun{TargetID: &targetID},
				Automation: models.Automation{SessionContinuity: models.AutomationSessionContinuityPerTarget},
			},
			want: true,
		},
		{
			name: "kill switch forces the per-run path",
			in: DispatchInput{
				Run:        models.AutomationRun{TargetID: &targetID},
				Automation: models.Automation{SessionContinuity: models.AutomationSessionContinuityPerTarget},
				KillSwitch: true,
			},
		},
		{
			name: "automation switched back to per_run",
			in: DispatchInput{
				Run:        models.AutomationRun{TargetID: &targetID},
				Automation: models.Automation{SessionContinuity: models.AutomationSessionContinuityPerRun},
			},
		},
		{
			name: "run created before continuity was enabled has no target",
			in: DispatchInput{
				Run:        models.AutomationRun{},
				Automation: models.Automation{SessionContinuity: models.AutomationSessionContinuityPerTarget},
			},
		},
	}
	d := NewTargetDispatcher(nil, nil, nil, nil, nil, nil, nil, zerolog.Nop())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, d.Applies(tt.in), "precedence is kill switch, then the automation row, then the run's target")
		})
	}
}

func TestTargetDispatcher_Decide(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	head := "2222222222222222222222222222222222222222"
	reviewed := "1111111111111111111111111111111111111111"
	key := "snapshots/gen"
	container := "container-1"
	node := "node-a"
	pending := "snapshots/pending"
	claude := "claude-code"
	codex := "codex"
	high := models.ReasoningEffortHigh
	userA := uuid.New()
	userB := uuid.New()

	retired := func(r models.AutomationTargetRetiredReason) *models.AutomationTargetRetiredReason { return &r }
	continuation := func(r models.AutomationRunContinuationReason) *models.AutomationRunContinuationReason { return &r }

	readySession := func() models.Session {
		checkpointed := now.Add(-time.Hour)
		return models.Session{
			ID: uuid.New(), Status: models.SessionStatusIdle, AgentType: models.AgentType(claude),
			SnapshotKey: &key, SandboxState: models.SandboxStateSnapshotted,
			CheckpointKind: models.CheckpointKindTurnComplete, CheckpointedAt: &checkpointed,
		}
	}
	activeGeneration := func() models.AutomationTargetSession {
		return models.AutomationTargetSession{ID: uuid.New(), Generation: 2, TurnCount: 3, LastReviewedHeadSHA: &reviewed}
	}
	template := func() *models.Session { return &models.Session{AgentType: models.AgentType(claude)} }

	tests := []struct {
		name          string
		automation    models.Automation
		template      *models.Session
		expectedUser  *uuid.UUID
		github        automationRunGitHubContext
		generation    models.AutomationTargetSession
		hasGeneration bool
		session       func() models.Session
		isPush        bool
		maxAge        time.Duration
		want          continuationDecision
	}{
		{
			name: "no generation goes fresh",
			want: freshDecision(models.AutomationRunContinuationReasonNoGeneration, nil),
		},
		{
			name: "push at the reviewed head is a duplicate", hasGeneration: true, generation: activeGeneration(),
			github: automationRunGitHubContext{HeadSHA: reviewed}, session: readySession, isPush: true,
			want: continuationDecision{kind: decisionSkip, outcome: models.AutomationRunOutcomeDuplicateHead, note: "head already reviewed by this conversation"},
		},
		{
			name: "comment at the reviewed head still executes", hasGeneration: true, generation: activeGeneration(),
			github: automationRunGitHubContext{HeadSHA: reviewed}, session: readySession, template: template(),
			want: continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationContinued},
		},
		{
			name: "archived session retires as unavailable", hasGeneration: true, generation: activeGeneration(),
			session: func() models.Session { s := readySession(); s.ArchivedAt = &now; return s },
			want:    retireDecision(models.AutomationTargetRetiredSessionUnavailable),
		},
		{
			name: "missing session retires as unavailable", hasGeneration: true, generation: activeGeneration(),
			session: func() models.Session { return models.Session{} },
			want:    retireDecision(models.AutomationTargetRetiredSessionUnavailable),
		},
		{
			name: "running session waits", hasGeneration: true, generation: activeGeneration(),
			session: func() models.Session { s := readySession(); s.Status = models.SessionStatusRunning; return s },
			want:    continuationDecision{kind: decisionWait, note: "generation session is running"},
		},
		{
			name: "pending session is not resumable", hasGeneration: true, generation: activeGeneration(),
			session: func() models.Session { s := readySession(); s.Status = models.SessionStatusPending; return s },
			want:    retireDecision(models.AutomationTargetRetiredNotResumable),
		},
		{
			name: "agent type change retires", hasGeneration: true, generation: activeGeneration(),
			session: readySession, template: &models.Session{AgentType: models.AgentType(codex)},
			want: retireDecision(models.AutomationTargetRetiredAgentConfigChanged),
		},
		{
			name: "reasoning effort change retires", hasGeneration: true, generation: activeGeneration(),
			automation: models.Automation{ReasoningEffort: &high}, session: readySession, template: template(),
			want: retireDecision(models.AutomationTargetRetiredAgentConfigChanged),
		},
		{
			name: "executing user change retires", hasGeneration: true, generation: activeGeneration(),
			session:  func() models.Session { s := readySession(); s.TriggeredByUserID = &userA; return s },
			template: template(), expectedUser: &userB,
			want: retireDecision(models.AutomationTargetRetiredIdentityChanged),
		},
		{
			name: "same executing user continues", hasGeneration: true, generation: activeGeneration(),
			session:  func() models.Session { s := readySession(); s.TriggeredByUserID = &userA; return s },
			template: template(), expectedUser: &userA,
			want: continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationContinued},
		},
		{
			name: "destroyed sandbox with a usable checkpoint continues", hasGeneration: true, generation: activeGeneration(),
			session:  func() models.Session { s := readySession(); s.SandboxState = models.SandboxStateDestroyed; return s },
			template: template(),
			want:     continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationContinued},
		},
		{
			name: "checkpoint without a timestamp fails the age bound", hasGeneration: true, generation: activeGeneration(),
			session:  func() models.Session { s := readySession(); s.CheckpointedAt = nil; return s },
			template: template(), maxAge: time.Hour,
			want: reconstructDecision(models.AutomationRunContinuationReasonSnapshotMissing),
		},
		{
			name: "base retarget retires", hasGeneration: true,
			generation: func() models.AutomationTargetSession {
				g := activeGeneration()
				ref := "main"
				g.LastBaseRef = &ref
				return g
			}(),
			github: automationRunGitHubContext{HeadSHA: head, BaseBranch: "release"}, session: readySession, template: template(),
			want: retireDecision(models.AutomationTargetRetiredBaseRetargeted),
		},
		{
			name: "turn limit retires", hasGeneration: true,
			generation: func() models.AutomationTargetSession {
				g := activeGeneration()
				g.TurnCount = AutomationTurnLimit
				return g
			}(),
			session: readySession, template: template(),
			want: retireDecision(models.AutomationTargetRetiredTurnLimit),
		},
		{
			name: "oversized checkpoint retires", hasGeneration: true, generation: activeGeneration(),
			session: func() models.Session {
				s := readySession()
				s.CheckpointSizeBytes = AutomationCheckpointSizeLimit + 1
				return s
			},
			template: template(),
			want:     retireDecision(models.AutomationTargetRetiredSnapshotTooLarge),
		},
		{
			name: "fresh pending upload retries", hasGeneration: true, generation: activeGeneration(),
			session: func() models.Session {
				s := readySession()
				setAt := now.Add(-time.Minute)
				s.PendingSnapshotKey = &pending
				s.PendingSnapshotSetAt = &setAt
				return s
			},
			template: template(),
			want:     continuationDecision{kind: decisionRetry, retryAfter: automationPendingSnapshotRetry, maxWait: automationPendingSnapshotGrace + automationPendingSnapshotRetry, note: "snapshot upload in flight"},
		},
		{
			name: "stale pending upload with a published key continues", hasGeneration: true, generation: activeGeneration(),
			session: func() models.Session {
				s := readySession()
				setAt := now.Add(-10 * time.Minute)
				s.PendingSnapshotKey = &pending
				s.PendingSnapshotSetAt = &setAt
				return s
			},
			template: template(),
			want:     continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationContinued},
		},
		{
			name: "stale pending upload without a key waits for the reaper", hasGeneration: true, generation: activeGeneration(),
			session: func() models.Session {
				s := readySession()
				setAt := now.Add(-10 * time.Minute)
				s.PendingSnapshotKey = &pending
				s.PendingSnapshotSetAt = &setAt
				s.SnapshotKey = nil
				return s
			},
			template: template(),
			want:     continuationDecision{kind: decisionRetry, retryAfter: automationPendingSnapshotRetry, maxWait: 2 * automationPendingSnapshotGrace, note: "snapshot upload stranded; waiting for the reaper"},
		},
		{
			name: "live container continues", hasGeneration: true, generation: activeGeneration(),
			session: func() models.Session {
				s := readySession()
				s.ContainerID = &container
				s.WorkerNodeID = &node
				s.SandboxState = models.SandboxStateRunning
				s.SnapshotKey = nil
				return s
			},
			template: template(),
			want:     continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationContinued},
		},
		{
			name: "destroyed sandbox without a checkpoint reconstructs", hasGeneration: true, generation: activeGeneration(),
			session: func() models.Session {
				s := readySession()
				s.SandboxState = models.SandboxStateDestroyed
				s.SnapshotKey = nil
				return s
			},
			template: template(),
			want:     continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationReconstructed, reason: continuation(models.AutomationRunContinuationReasonSandboxDestroyed)},
		},
		{
			name: "sandbox_state none with no snapshot reconstructs", hasGeneration: true, generation: activeGeneration(),
			session: func() models.Session {
				s := readySession()
				s.SandboxState = models.SandboxStateNone
				s.SnapshotKey = nil
				return s
			},
			template: template(),
			want:     continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationReconstructed, reason: continuation(models.AutomationRunContinuationReasonSnapshotMissing)},
		},
		{
			name: "snapshot of an unusable checkpoint kind reconstructs", hasGeneration: true, generation: activeGeneration(),
			session:  func() models.Session { s := readySession(); s.CheckpointKind = models.CheckpointKindNone; return s },
			template: template(),
			want:     continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationReconstructed, reason: continuation(models.AutomationRunContinuationReasonSnapshotMissing)},
		},
		{
			name: "snapshot older than the age bound reconstructs", hasGeneration: true, generation: activeGeneration(),
			session: readySession, template: template(), maxAge: 30 * time.Minute,
			want: continuationDecision{kind: decisionProceed, mode: models.AutomationRunContinuationReconstructed, reason: continuation(models.AutomationRunContinuationReasonSnapshotMissing)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			d := NewTargetDispatcher(nil, nil, nil, nil, nil, nil, nil, zerolog.Nop())
			d.now = func() time.Time { return now }
			d.SetMaxSnapshotAge(tt.maxAge)
			session := models.Session{}
			if tt.session != nil {
				session = tt.session()
			}
			automation := tt.automation
			got := d.decide(automation, tt.template, tt.expectedUser, tt.github, tt.generation, tt.hasGeneration, session, tt.isPush)
			require.Equal(t, tt.want, got, "decision should follow the design's compatibility and readiness tables")
			if tt.want.retireReason != nil {
				require.Equal(t, retired(*tt.want.retireReason), got.retireReason, "retire reason should match")
				require.Equal(t, continuation(models.ContinuationReasonForRetirement(*tt.want.retireReason)), got.reason, "a retirement carries the matching continuation reason")
			}
		})
	}
}

func TestBaselineHead(t *testing.T) {
	t.Parallel()
	checkpoint := "cccc"
	reviewed := "rrrr"
	key := "k"
	other := "other"
	native := "agent-session"
	tests := []struct {
		name       string
		generation models.AutomationTargetSession
		session    models.Session
		thread     models.SessionThread
		mode       models.AutomationRunContinuationMode
		want       *string
	}{
		{
			name:       "coherent checkpoint with native context uses the checkpoint head",
			generation: models.AutomationTargetSession{CheckpointHeadSHA: &checkpoint, CheckpointSnapshotKey: &key, LastReviewedHeadSHA: &reviewed},
			session:    models.Session{SnapshotKey: &key},
			thread:     models.SessionThread{AgentSessionID: &native},
			mode:       models.AutomationRunContinuationContinued,
			want:       &checkpoint,
		},
		{
			name:       "key mismatch is treated as null provenance",
			generation: models.AutomationTargetSession{CheckpointHeadSHA: &checkpoint, CheckpointSnapshotKey: &other, LastReviewedHeadSHA: &reviewed},
			session:    models.Session{SnapshotKey: &key},
			thread:     models.SessionThread{AgentSessionID: &native},
			mode:       models.AutomationRunContinuationContinued,
			want:       &reviewed,
		},
		{
			name:       "no native context falls back to the reviewed head",
			generation: models.AutomationTargetSession{CheckpointHeadSHA: &checkpoint, CheckpointSnapshotKey: &key, LastReviewedHeadSHA: &reviewed},
			session:    models.Session{SnapshotKey: &key},
			mode:       models.AutomationRunContinuationContinued,
			want:       &reviewed,
		},
		{
			name:       "null provenance uses the reviewed head",
			generation: models.AutomationTargetSession{LastReviewedHeadSHA: &reviewed},
			session:    models.Session{SnapshotKey: &key, AgentSessionID: &native},
			mode:       models.AutomationRunContinuationContinued,
			want:       &reviewed,
		},
		{
			name:       "reconstructed turn uses the reviewed head",
			generation: models.AutomationTargetSession{CheckpointHeadSHA: &checkpoint, CheckpointSnapshotKey: &key, LastReviewedHeadSHA: &reviewed},
			session:    models.Session{SnapshotKey: &key, AgentSessionID: &native},
			mode:       models.AutomationRunContinuationReconstructed,
			want:       &reviewed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, baselineHead(tt.generation, tt.session, tt.thread, tt.mode), "baseline follows checkpoint coherence and native resume")
		})
	}
}

func TestHeadLookupBackoff(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		waiting time.Duration
		want    time.Duration
	}{
		{name: "first retry", waiting: 0, want: 30 * time.Second},
		{name: "after one minute", waiting: time.Minute, want: time.Minute},
		{name: "after five minutes", waiting: 5 * time.Minute, want: 4 * time.Minute},
		{name: "capped at ten minutes", waiting: 3 * time.Hour, want: 10 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			since := now.Add(-tt.waiting)
			run := models.AutomationRun{TriggeredAt: since.Add(-time.Hour), WaitStartedAt: &since}
			require.Equal(t, tt.want, headLookupBackoff(now, run), "backoff doubles from 30s to 10m over the wait")
		})
	}
	run := models.AutomationRun{TriggeredAt: now.Add(-2 * time.Minute)}
	require.Equal(t, 2*time.Minute, headLookupBackoff(now, run), "without a wait start the trigger time anchors the backoff")
}

func TestLifecycleAllowsRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state models.AutomationTargetLifecycleState
		event models.AutomationGitHubEvent
		want  bool
	}{
		{models.AutomationTargetLifecycleOpen, models.AutomationGitHubEventIssueCommentCreated, true},
		{models.AutomationTargetLifecycleMerged, models.AutomationGitHubEventPullRequestMerged, true},
		{models.AutomationTargetLifecycleMerged, models.AutomationGitHubEventPullRequestUpdated, false},
		{models.AutomationTargetLifecycleClosed, models.AutomationGitHubEventPullRequestMerged, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.state)+"/"+string(tt.event), func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, lifecycleAllowsRun(tt.state, tt.event), "only open targets and the merged run on a merged target execute")
		})
	}
}

func TestAutomationExecutingUser(t *testing.T) {
	t.Parallel()
	creator := uuid.New()
	tests := []struct {
		name       string
		automation models.Automation
		want       *uuid.UUID
		wantErr    bool
	}{
		{name: "org scope runs as nobody", automation: models.Automation{IdentityScope: models.AutomationIdentityScopeOrg}},
		{name: "default scope is org", automation: models.Automation{}},
		{name: "personal scope runs as the creator", automation: models.Automation{IdentityScope: models.AutomationIdentityScopePersonal, CreatedBy: &creator}, want: &creator},
		{name: "personal scope without a creator fails", automation: models.Automation{IdentityScope: models.AutomationIdentityScopePersonal}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := automationExecutingUser(tt.automation)
			if tt.wantErr {
				require.Error(t, err, "identity must be resolvable")
				return
			}
			require.NoError(t, err, "identity resolves")
			require.Equal(t, tt.want, got, "identity follows the current scope")
		})
	}
}

func TestPayloadCarriesRun(t *testing.T) {
	t.Parallel()
	runID := uuid.New()
	tests := []struct {
		name    string
		payload AutomationTurnJobPayload
		want    bool
	}{
		{name: "payload for this run", payload: AutomationTurnJobPayload{AutomationRunID: runID.String()}, want: true},
		{name: "payload for another run", payload: AutomationTurnJobPayload{AutomationRunID: uuid.NewString()}},
		{name: "payload without a run", payload: AutomationTurnJobPayload{SessionID: uuid.NewString()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tt.payload)
			require.NoError(t, err, "payload should marshal")
			require.Equal(t, tt.want, payloadCarriesRun(raw, runID), "conflict lookup accepts only this run's job")
		})
	}
	require.False(t, payloadCarriesRun([]byte("not json"), runID), "malformed payload is rejected")
}

func TestAutomationTurnPrompt(t *testing.T) {
	t.Parallel()
	previous := "1111"
	continued := AutomationTurnPrompt(AutomationTurnPromptInput{Goal: "Review UI changes", TurnNumber: 3, Mode: models.AutomationRunContinuationContinued, HeadSHA: "2222", PreviousHeadSHA: &previous, BaseBranch: "main"})
	require.Contains(t, continued, "Review UI changes", "prompt starts with the goal")
	require.Contains(t, continued, "- Turn: 3", "prompt states the turn")
	require.Contains(t, continued, "- Baseline head: 1111", "prompt states the baseline")
	require.Contains(t, continued, "- Head: 2222", "prompt states the head")
	require.Contains(t, continued, "state which earlier findings the new push resolved", "continued prompt asks for a delta review")

	reconstructed := AutomationTurnPrompt(AutomationTurnPromptInput{Goal: "g", TurnNumber: 2, Mode: models.AutomationRunContinuationReconstructed, HeadSHA: "2222"})
	require.Contains(t, reconstructed, "- Baseline head: none", "missing baseline renders as none")
	require.Contains(t, reconstructed, "earlier context for this pull request is unavailable", "reconstructed prompt asks for a full review")
	fresh := AutomationTurnPrompt(AutomationTurnPromptInput{Goal: "g", TurnNumber: 1, Mode: models.AutomationRunContinuationFresh, HeadSHA: "2222"})
	require.Contains(t, fresh, "first turn of this pull request's review conversation", "fresh prompt asks for a full review")
}

func TestGithubContextFromRun(t *testing.T) {
	t.Parallel()
	run := models.AutomationRun{ConfigSnapshot: json.RawMessage(`{"github":{"repository":"acme/web","pull_request_number":42,"head_sha":"abc","base_branch":"main"}}`)}
	got, err := githubContextFromRun(run)
	require.NoError(t, err, "snapshot should parse")
	require.Equal(t, automationRunGitHubContext{Repository: "acme/web", PullRequestNumber: 42, HeadSHA: "abc", BaseBranch: "main"}, got, "github context round-trips")
	empty, err := githubContextFromRun(models.AutomationRun{})
	require.NoError(t, err, "empty snapshot is allowed")
	require.Equal(t, automationRunGitHubContext{}, empty, "empty snapshot yields no context")
	_, err = githubContextFromRun(models.AutomationRun{ConfigSnapshot: json.RawMessage(`{`)})
	require.Error(t, err, "malformed snapshot is rejected")
	require.Equal(t, "42", targetKeyForRun(run, got), "target key is the PR number")
}
