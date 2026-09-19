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
	d := NewTargetDispatcher(nil, nil, nil, nil, nil, nil, zerolog.Nop())
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
			template: &models.Session{AgentType: models.AgentType(claude), TriggeredByUserID: &userB},
			want:     retireDecision(models.AutomationTargetRetiredIdentityChanged),
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
			want:     continuationDecision{kind: decisionRetry, retryAfter: automationPendingSnapshotRetry, note: "snapshot upload in flight"},
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
			want:     continuationDecision{kind: decisionRetry, retryAfter: automationPendingSnapshotRetry, note: "snapshot upload stranded; waiting for the reaper"},
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
			name: "destroyed sandbox reconstructs", hasGeneration: true, generation: activeGeneration(),
			session:  func() models.Session { s := readySession(); s.SandboxState = models.SandboxStateDestroyed; return s },
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
			d := NewTargetDispatcher(nil, nil, nil, nil, nil, nil, zerolog.Nop())
			d.now = func() time.Time { return now }
			d.SetMaxSnapshotAge(tt.maxAge)
			session := models.Session{}
			if tt.session != nil {
				session = tt.session()
			}
			automation := tt.automation
			got := d.decide(DispatchInput{Automation: automation, SessionTemplate: tt.template}, tt.github, models.AutomationTarget{}, tt.generation, tt.hasGeneration, session, tt.isPush)
			require.Equal(t, tt.want, got, "decision should follow the design's compatibility and readiness tables")
			if got.retireReason != nil {
				require.Equal(t, retired(*got.retireReason), got.retireReason, "retire reason should be set")
				require.NotNil(t, got.reason, "a retirement carries the matching continuation reason")
			}
		})
	}
}

func TestBaselineHead(t *testing.T) {
	t.Parallel()
	checkpoint := "cccc"
	reviewed := "rrrr"
	key := "k"
	tests := []struct {
		name       string
		generation models.AutomationTargetSession
		mode       models.AutomationRunContinuationMode
		want       *string
	}{
		{name: "continued turn uses the checkpoint head", generation: models.AutomationTargetSession{CheckpointHeadSHA: &checkpoint, CheckpointSnapshotKey: &key, LastReviewedHeadSHA: &reviewed}, mode: models.AutomationRunContinuationContinued, want: &checkpoint},
		{name: "continued turn without provenance uses the reviewed head", generation: models.AutomationTargetSession{LastReviewedHeadSHA: &reviewed}, mode: models.AutomationRunContinuationContinued, want: &reviewed},
		{name: "reconstructed turn uses the reviewed head", generation: models.AutomationTargetSession{CheckpointHeadSHA: &checkpoint, CheckpointSnapshotKey: &key, LastReviewedHeadSHA: &reviewed}, mode: models.AutomationRunContinuationReconstructed, want: &reviewed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, baselineHead(tt.generation, tt.mode), "baseline follows checkpoint coherence")
		})
	}
}

func TestPayloadCarriesRun(t *testing.T) {
	t.Parallel()
	runID := uuid.New()
	sessionID := uuid.New()
	tests := []struct {
		name    string
		payload map[string]string
		want    bool
	}{
		{name: "continue payload for this run", payload: map[string]string{"automation_run_id": runID.String()}, want: true},
		{name: "continue payload for another run", payload: map[string]string{"automation_run_id": uuid.NewString()}},
		{name: "fresh payload for this run's session", payload: map[string]string{"session_id": sessionID.String()}, want: true},
		{name: "fresh payload for another session", payload: map[string]string{"session_id": uuid.NewString()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tt.payload)
			require.NoError(t, err, "payload should marshal")
			require.Equal(t, tt.want, payloadCarriesRun(raw, runID, sessionID), "conflict lookup accepts only this run's job")
		})
	}
	require.False(t, payloadCarriesRun([]byte("not json"), runID, sessionID), "malformed payload is rejected")
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
