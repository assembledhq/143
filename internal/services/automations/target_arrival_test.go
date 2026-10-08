package automations

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type fakeArrivalTargetStore struct {
	target      models.AutomationTarget
	adopted     []string
	adoptedAt   []*time.Time
	lifecycle   []models.AutomationTargetLifecycleState
	observedAt  []*time.Time
	pendingAt   []time.Time
	touched     []time.Time
	lockCalls   int
	nextEpoch   int
	lockOrCreat func() (models.AutomationTarget, error)
}

func (f *fakeArrivalTargetStore) LockOrCreate(_ context.Context, _ pgx.Tx, _, _, _ uuid.UUID, _ models.AutomationTargetKind, _ string) (models.AutomationTarget, error) {
	f.lockCalls++
	if f.lockOrCreat != nil {
		return f.lockOrCreat()
	}
	return f.target, nil
}

func (f *fakeArrivalTargetStore) AdoptHead(_ context.Context, _ pgx.Tx, _, _ uuid.UUID, headSHA string, updatedAt *time.Time) (int, error) {
	f.adopted = append(f.adopted, headSHA)
	f.adoptedAt = append(f.adoptedAt, updatedAt)
	f.nextEpoch++
	return f.target.HeadEpoch + f.nextEpoch, nil
}

func (f *fakeArrivalTargetStore) TouchObservedHead(_ context.Context, _ pgx.Tx, _, _ uuid.UUID, headSHA string, updatedAt time.Time) error {
	f.touched = append(f.touched, updatedAt)
	return nil
}

func (f *fakeArrivalTargetStore) MarkHeadResolutionPending(_ context.Context, _ pgx.Tx, _, _ uuid.UUID, deadline time.Time) error {
	f.pendingAt = append(f.pendingAt, deadline)
	return nil
}

func (f *fakeArrivalTargetStore) SetLifecycle(_ context.Context, _ db.DBTX, _, _ uuid.UUID, state models.AutomationTargetLifecycleState) error {
	f.lifecycle = append(f.lifecycle, state)
	return nil
}

func (f *fakeArrivalTargetStore) SetLifecycleObserved(_ context.Context, _ db.DBTX, _, _ uuid.UUID, state models.AutomationTargetLifecycleState, observedAt *time.Time) error {
	f.lifecycle = append(f.lifecycle, state)
	f.observedAt = append(f.observedAt, observedAt)
	return nil
}

type fakeArrivalRunStore struct {
	arrivals      map[uuid.UUID]db.AutomationRunArrival
	terminalized  map[uuid.UUID]models.AutomationRunOutcomeReason
	superseded    []int
	waiting       int
	markedWaiting []uuid.UUID
}

func newFakeArrivalRunStore() *fakeArrivalRunStore {
	return &fakeArrivalRunStore{arrivals: map[uuid.UUID]db.AutomationRunArrival{}, terminalized: map[uuid.UUID]models.AutomationRunOutcomeReason{}}
}

func (f *fakeArrivalRunStore) RecordArrival(_ context.Context, _ pgx.Tx, _, runID uuid.UUID, arrival db.AutomationRunArrival) error {
	f.arrivals[runID] = arrival
	return nil
}

func (f *fakeArrivalRunStore) TerminalizeUnstarted(_ context.Context, _ db.DBTX, _, runID uuid.UUID, outcome models.AutomationRunOutcomeReason, _ *uuid.UUID, _ string) (bool, error) {
	f.terminalized[runID] = outcome
	return true, nil
}

func (f *fakeArrivalRunStore) SupersedeWaitingPush(_ context.Context, _ pgx.Tx, _, _, _ uuid.UUID, newEpoch int) (int64, error) {
	f.superseded = append(f.superseded, newEpoch)
	return 1, nil
}

func (f *fakeArrivalRunStore) CountWaiting(_ context.Context, _ db.DBTX, _, _ uuid.UUID) (int, error) {
	return f.waiting, nil
}

func (f *fakeArrivalRunStore) MarkWaiting(_ context.Context, _ db.DBTX, _, runID uuid.UUID) (bool, error) {
	f.markedWaiting = append(f.markedWaiting, runID)
	return true, nil
}

func TestGitHubEventTriggerService_PerTargetArrival(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	observed := "1111111111111111111111111111111111111111"
	newer := "2222222222222222222222222222222222222222"
	authoritative := models.AutomationRunHeadAuthoritative
	ambiguous := models.AutomationRunHeadAmbiguous
	intPtr := func(v int) *int { return &v }
	timePtr := func(v time.Time) *time.Time { return &v }

	openTarget := func() models.AutomationTarget {
		return models.AutomationTarget{
			ID: uuid.New(), LifecycleState: models.AutomationTargetLifecycleOpen,
			ObservedHeadSHA: &observed, ObservedHeadUpdatedAt: timePtr(base), HeadEpoch: 3,
		}
	}

	tests := []struct {
		name           string
		target         models.AutomationTarget
		waiting        int
		req            GitHubEventTriggerRequest
		wantJob        bool
		wantOutcome    models.AutomationRunOutcomeReason
		wantEpoch      *int
		wantResolution *models.AutomationRunHeadResolution
		wantAdopted    []string
		wantSuperseded []int
		wantPending    bool
		wantTouched    []time.Time
		wantLifecycle  []models.AutomationTargetLifecycleState
	}{
		{
			name:   "strictly newer push adopts the head and supersedes older waiting pushes",
			target: openTarget(),
			req: GitHubEventTriggerRequest{
				Event: models.AutomationGitHubEventPullRequestUpdated, PullRequestAction: "synchronize",
				HeadSHA: newer, PullRequestUpdatedAt: timePtr(base.Add(time.Second)),
			},
			wantJob: true, wantEpoch: intPtr(4), wantResolution: &authoritative,
			wantAdopted: []string{newer}, wantSuperseded: []int{4},
			wantLifecycle: []models.AutomationTargetLifecycleState{models.AutomationTargetLifecycleOpen},
		},
		{
			name:   "same-head delivery joins the observed epoch",
			target: openTarget(),
			req: GitHubEventTriggerRequest{
				Event: models.AutomationGitHubEventPullRequestUpdated, PullRequestAction: "edited",
				HeadSHA: observed, PullRequestUpdatedAt: timePtr(base.Add(time.Minute)),
			},
			wantJob: true, wantEpoch: intPtr(3), wantResolution: &authoritative, wantTouched: []time.Time{base.Add(time.Minute)},
			wantLifecycle: []models.AutomationTargetLifecycleState{models.AutomationTargetLifecycleOpen},
		},
		{
			name:   "same-head force-push with a newer timestamp advances the watermark",
			target: openTarget(),
			req: GitHubEventTriggerRequest{
				Event: models.AutomationGitHubEventPullRequestUpdated, PullRequestAction: "synchronize",
				HeadSHA: observed, PullRequestUpdatedAt: timePtr(base.Add(2 * time.Second)),
			},
			wantJob: true, wantEpoch: intPtr(3), wantResolution: &authoritative, wantTouched: []time.Time{base.Add(2 * time.Second)},
			wantLifecycle: []models.AutomationTargetLifecycleState{models.AutomationTargetLifecycleOpen},
		},
		{
			name:   "older push with a different head is skipped as stale",
			target: openTarget(),
			req: GitHubEventTriggerRequest{
				Event: models.AutomationGitHubEventPullRequestUpdated, PullRequestAction: "synchronize",
				HeadSHA: newer, PullRequestUpdatedAt: timePtr(base.Add(-time.Second)),
			},
			wantOutcome:   models.AutomationRunOutcomeStaleHead,
			wantLifecycle: []models.AutomationTargetLifecycleState{models.AutomationTargetLifecycleOpen},
		},
		{
			name:   "equal timestamp with a different head is ambiguous",
			target: openTarget(),
			req: GitHubEventTriggerRequest{
				Event: models.AutomationGitHubEventPullRequestUpdated, PullRequestAction: "synchronize",
				HeadSHA: newer, PullRequestUpdatedAt: timePtr(base),
			},
			wantJob: true, wantResolution: &ambiguous, wantPending: true,
			wantLifecycle: []models.AutomationTargetLifecycleState{models.AutomationTargetLifecycleOpen},
		},
		{
			name:   "push without a timestamp is ambiguous",
			target: openTarget(),
			req: GitHubEventTriggerRequest{
				Event: models.AutomationGitHubEventPullRequestUpdated, PullRequestAction: "synchronize", HeadSHA: newer,
			},
			wantJob: true, wantResolution: &ambiguous, wantPending: true,
		},
		{
			name:   "comment at an unobserved head executes with a null epoch",
			target: openTarget(),
			req: GitHubEventTriggerRequest{
				Event: models.AutomationGitHubEventIssueCommentCreated, HeadSHA: newer, Body: "looks good",
			},
			wantJob: true,
		},
		{
			name: "first push on a new target is authoritative",
			target: models.AutomationTarget{
				ID: uuid.New(), LifecycleState: models.AutomationTargetLifecycleOpen,
			},
			req: GitHubEventTriggerRequest{
				Event: models.AutomationGitHubEventPullRequestOpened, PullRequestAction: "opened",
				HeadSHA: newer, PullRequestUpdatedAt: timePtr(base),
			},
			wantJob: true, wantEpoch: intPtr(1), wantResolution: &authoritative, wantAdopted: []string{newer},
			wantLifecycle: []models.AutomationTargetLifecycleState{models.AutomationTargetLifecycleOpen},
		},
		{
			name: "late event on a closed target is skipped",
			target: models.AutomationTarget{
				ID: uuid.New(), LifecycleState: models.AutomationTargetLifecycleClosed,
			},
			req: GitHubEventTriggerRequest{
				Event: models.AutomationGitHubEventIssueCommentCreated, HeadSHA: newer,
			},
			wantOutcome: models.AutomationRunOutcomePRClosed,
		},
		{
			name: "reopened delivery reopens the target and executes",
			target: models.AutomationTarget{
				ID: uuid.New(), LifecycleState: models.AutomationTargetLifecycleClosed,
			},
			req: GitHubEventTriggerRequest{
				Event: models.AutomationGitHubEventPullRequestUpdated, PullRequestAction: "reopened",
				HeadSHA: newer, PullRequestUpdatedAt: timePtr(base),
			},
			wantJob: true, wantEpoch: intPtr(1), wantResolution: &authoritative, wantAdopted: []string{newer},
			wantLifecycle: []models.AutomationTargetLifecycleState{models.AutomationTargetLifecycleOpen, models.AutomationTargetLifecycleOpen},
		},
		{
			name: "reopened delivery without a timestamp reopens the target but leaves the evidence unknown",
			target: models.AutomationTarget{
				ID: uuid.New(), LifecycleState: models.AutomationTargetLifecycleClosed,
			},
			req: GitHubEventTriggerRequest{
				Event: models.AutomationGitHubEventPullRequestUpdated, PullRequestAction: "reopened", HeadSHA: newer,
			},
			wantJob:       true,
			wantLifecycle: []models.AutomationTargetLifecycleState{models.AutomationTargetLifecycleOpen},
		},
		{
			name:   "merged event marks the target merged and executes as the final turn",
			target: openTarget(),
			req: GitHubEventTriggerRequest{
				Event: models.AutomationGitHubEventPullRequestMerged, PullRequestAction: "closed", HeadSHA: observed,
			},
			wantJob: true, wantEpoch: intPtr(3), wantResolution: &authoritative,
			wantLifecycle: []models.AutomationTargetLifecycleState{models.AutomationTargetLifecycleMerged},
		},
		{
			name:    "waiting cap fails the run with wait_overflow",
			target:  openTarget(),
			waiting: automationTargetWaitingCap,
			req: GitHubEventTriggerRequest{
				Event: models.AutomationGitHubEventIssueCommentCreated, HeadSHA: observed,
			},
			wantOutcome: models.AutomationRunOutcomeWaitOverflow,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID := uuid.New()
			repoID := uuid.New()
			automationID := uuid.New()
			store := &fakeGitHubAutomationStore{automations: []models.Automation{{
				ID: automationID, OrgID: orgID, RepositoryID: &repoID, Name: "Front-end review", Goal: "Review",
				ExecutionMode: models.AutomationExecutionModeSequential, MaxConcurrent: 1, BaseBranch: "main",
				PublishPolicy: models.AutomationPublishPolicyNone, SessionContinuity: models.AutomationSessionContinuityPerTarget,
			}}}
			runs := &fakeGitHubAutomationRunStore{}
			jobs := &fakeGitHubAutomationJobStore{}
			targets := &fakeArrivalTargetStore{target: tt.target}
			arrivals := newFakeArrivalRunStore()
			arrivals.waiting = tt.waiting
			service := NewGitHubEventTriggerService(store, runs, jobs, &pagerDutyTxStarterFake{}, zerolog.Nop())
			service.now = func() time.Time { return base }
			service.SetTargetStores(targets, arrivals)

			req := tt.req
			req.OrgID = orgID
			req.RepositoryID = repoID
			req.Repository = "acme/web"
			req.PullRequestNumber = 42
			req.ProviderEventID = "delivery-" + uuid.NewString()
			require.NoError(t, service.TriggerGitHubEvent(context.Background(), req), "trigger should succeed")
			require.Len(t, runs.runs, 1, "one run is created")
			runID := runs.runs[0].ID
			require.Equal(t, 1, targets.lockCalls, "arrival locks the target once")

			arrival, ok := arrivals.arrivals[runID]
			require.True(t, ok, "arrival is recorded on the run")
			require.Equal(t, tt.target.ID, arrival.TargetID, "run is attached to the target")
			require.Equal(t, tt.req.PullRequestAction, arrival.GitHubAction, "run records the GitHub action")
			require.Equal(t, tt.req.PullRequestUpdatedAt, arrival.PullRequestUpdatedAt, "run records the PR timestamp")
			require.Equal(t, tt.wantEpoch, arrival.HeadEpoch, "run carries the decided epoch")
			require.Equal(t, tt.wantResolution, arrival.HeadResolution, "run carries the decided resolution")
			require.Equal(t, tt.wantAdopted, targets.adopted, "target adopts exactly the newer heads")
			require.Equal(t, tt.wantSuperseded, arrivals.superseded, "older waiting pushes are superseded only by a newer epoch")
			require.Equal(t, tt.wantPending, len(targets.pendingAt) == 1, "ambiguity marks the target pending")
			if tt.wantPending {
				require.Equal(t, base.Add(automationHeadAmbiguityWindow), targets.pendingAt[0], "ambiguity deadline is 30 minutes out")
				require.Equal(t, []uuid.UUID{runID}, arrivals.markedWaiting, "an ambiguous candidate waits visibly")
			} else {
				require.Empty(t, arrivals.markedWaiting, "only ambiguous candidates are marked waiting at arrival")
			}
			require.Equal(t, tt.wantTouched, targets.touched, "same-head deliveries advance the observed timestamp only when newer")
			require.Equal(t, tt.wantLifecycle, targets.lifecycle, "lifecycle transitions match, including the openness refresh a timestamped pull_request delivery provides")
			require.Len(t, targets.observedAt, len(tt.wantLifecycle), "every lifecycle write goes through the observed variant")
			for _, observed := range targets.observedAt {
				require.Equal(t, tt.req.PullRequestUpdatedAt, observed, "lifecycle evidence carries the delivery's timestamp, never the processing time; a missing timestamp leaves the evidence unknown")
			}
			if tt.wantJob {
				require.Len(t, jobs.jobs, 1, "dispatchable run enqueues the automation_run job")
				require.Empty(t, arrivals.terminalized, "dispatchable run is not terminalized")
			} else {
				require.Empty(t, jobs.jobs, "terminalized run enqueues nothing")
				require.Equal(t, tt.wantOutcome, arrivals.terminalized[runID], "run is terminalized with the expected outcome")
			}
		})
	}
}

func TestGitHubEventTriggerService_PerRunAutomationSkipsTargets(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	repoID := uuid.New()
	store := &fakeGitHubAutomationStore{automations: []models.Automation{{
		ID: uuid.New(), OrgID: orgID, RepositoryID: &repoID, Name: "Review PR", Goal: "Review",
		ExecutionMode: models.AutomationExecutionModeSequential, MaxConcurrent: 1, BaseBranch: "main",
	}}}
	runs := &fakeGitHubAutomationRunStore{}
	jobs := &fakeGitHubAutomationJobStore{}
	targets := &fakeArrivalTargetStore{}
	arrivals := newFakeArrivalRunStore()
	service := NewGitHubEventTriggerService(store, runs, jobs, &pagerDutyTxStarterFake{}, zerolog.Nop())
	service.SetTargetStores(targets, arrivals)

	require.NoError(t, service.TriggerGitHubEvent(context.Background(), GitHubEventTriggerRequest{
		OrgID: orgID, RepositoryID: repoID, Event: models.AutomationGitHubEventPullRequestUpdated,
		PullRequestAction: "synchronize", Repository: "acme/web", PullRequestNumber: 7, HeadSHA: "abc",
	}), "trigger should succeed")
	require.Equal(t, 0, targets.lockCalls, "per_run automations never touch targets")
	require.Empty(t, arrivals.arrivals, "per_run runs record no arrival")
	require.Len(t, jobs.jobs, 1, "per_run run is dispatched as before")
}
