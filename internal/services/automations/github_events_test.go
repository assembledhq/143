package automations

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type fakeGitHubAutomationStore struct {
	automations []models.Automation
	calls       []fakeListGitHubEventCall
	err         error
}

type fakeListGitHubEventCall struct {
	orgID        uuid.UUID
	repositoryID uuid.UUID
	event        models.AutomationGitHubEvent
}

func (f *fakeGitHubAutomationStore) ListEnabledByGitHubEvent(_ context.Context, orgID, repositoryID uuid.UUID, event models.AutomationGitHubEvent) ([]models.Automation, error) {
	f.calls = append(f.calls, fakeListGitHubEventCall{orgID: orgID, repositoryID: repositoryID, event: event})
	if f.err != nil {
		return nil, f.err
	}
	return f.automations, nil
}

type fakeGitHubAutomationRunStore struct {
	runs []models.AutomationRun
	err  error
}

func (f *fakeGitHubAutomationRunStore) CreateRun(_ context.Context, run *models.AutomationRun) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	run.ID = uuid.New()
	f.runs = append(f.runs, *run)
	return true, nil
}

type fakeGitHubAutomationJobStore struct {
	jobs      []fakeGitHubAutomationJob
	notified  []uuid.UUID
	err       error
	nextJobID uuid.UUID
}

type fakeGitHubAutomationJob struct {
	orgID     uuid.UUID
	queue     string
	jobType   string
	payload   any
	priority  int
	dedupeKey *string
}

func (f *fakeGitHubAutomationJobStore) Enqueue(_ context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	if f.err != nil {
		return uuid.Nil, f.err
	}
	jobID := f.nextJobID
	if jobID == uuid.Nil {
		jobID = uuid.New()
	}
	f.jobs = append(f.jobs, fakeGitHubAutomationJob{
		orgID: orgID, queue: queue, jobType: jobType, payload: payload, priority: priority, dedupeKey: dedupeKey,
	})
	return jobID, nil
}

func (f *fakeGitHubAutomationJobStore) Notify(_ context.Context, jobID uuid.UUID) {
	f.notified = append(f.notified, jobID)
}

func newTestService(store *fakeGitHubAutomationStore, runs *fakeGitHubAutomationRunStore, jobs *fakeGitHubAutomationJobStore) *GitHubEventTriggerService {
	return NewGitHubEventTriggerService(store, runs, jobs, zerolog.Nop())
}

func TestGitHubEventTriggerService_TriggersMatchingAutomations(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	automationID := uuid.New()
	jobID := uuid.New()
	store := &fakeGitHubAutomationStore{automations: []models.Automation{{
		ID: automationID, OrgID: orgID, RepositoryID: &repoID, Name: "Review PR", Goal: "Run a review",
		ExecutionMode: models.AutomationExecutionModeSequential, MaxConcurrent: 1, BaseBranch: "main",
		IdentityScope: models.AutomationIdentityScopeOrg,
	}}}
	runs := &fakeGitHubAutomationRunStore{}
	jobs := &fakeGitHubAutomationJobStore{nextJobID: jobID}
	service := NewGitHubEventTriggerService(store, runs, jobs, zerolog.Nop())

	err := service.TriggerGitHubEvent(context.Background(), GitHubEventTriggerRequest{
		OrgID: orgID, RepositoryID: repoID, Event: models.AutomationGitHubEventPullRequestOpened,
		Repository: "acme/api", PullRequestNumber: 42, PullRequestURL: "https://github.com/acme/api/pull/42",
		Actor: "octocat", Body: "please review",
	})
	require.NoError(t, err, "triggering a GitHub event should succeed")
	require.Equal(t, []fakeListGitHubEventCall{{
		orgID: orgID, repositoryID: repoID, event: models.AutomationGitHubEventPullRequestOpened,
	}}, store.calls, "service should list automations by org, repository, and event")
	require.Len(t, runs.runs, 1, "matching automation should create one run")
	require.Equal(t, models.AutomationTriggeredByGitHub, runs.runs[0].TriggeredBy, "run should record GitHub as the trigger source")
	require.Contains(t, runs.runs[0].GoalSnapshot, "Run a review", "goal snapshot should include the automation goal")
	require.Contains(t, runs.runs[0].GoalSnapshot, "PR #42", "goal snapshot should include pull request context")
	require.Len(t, jobs.jobs, 1, "matching automation should enqueue one worker job")
	require.Equal(t, models.JobTypeAutomationRun, jobs.jobs[0].jobType, "job type should dispatch the automation worker")
	require.Equal(t, []uuid.UUID{jobID}, jobs.notified, "created job should be notified")

	var config map[string]any
	require.NoError(t, json.Unmarshal(runs.runs[0].ConfigSnapshot, &config), "config snapshot should be valid JSON")
	require.Equal(t, string(models.AutomationIdentityScopeOrg), config["identity_scope"], "config snapshot should preserve automation identity scope")
	require.Equal(t, string(models.AutomationGitHubEventPullRequestOpened), config["github_event"], "config snapshot should include the GitHub event")
}

func TestGitHubEventTriggerService_NoMatchingAutomations(t *testing.T) {
	t.Parallel()

	store := &fakeGitHubAutomationStore{automations: nil}
	runs := &fakeGitHubAutomationRunStore{}
	jobs := &fakeGitHubAutomationJobStore{}
	service := newTestService(store, runs, jobs)

	err := service.TriggerGitHubEvent(context.Background(), GitHubEventTriggerRequest{
		OrgID: uuid.New(), RepositoryID: uuid.New(),
		Event: models.AutomationGitHubEventPullRequestOpened,
	})
	require.NoError(t, err, "no matching automations should not be an error")
	require.Empty(t, runs.runs, "no runs should be created when no automations match")
	require.Empty(t, jobs.jobs, "no jobs should be enqueued when no automations match")
}

func TestGitHubEventTriggerService_InvalidEvent(t *testing.T) {
	t.Parallel()

	store := &fakeGitHubAutomationStore{}
	runs := &fakeGitHubAutomationRunStore{}
	jobs := &fakeGitHubAutomationJobStore{}
	service := newTestService(store, runs, jobs)

	err := service.TriggerGitHubEvent(context.Background(), GitHubEventTriggerRequest{
		OrgID: uuid.New(), RepositoryID: uuid.New(),
		Event: models.AutomationGitHubEvent("github.unknown.event"),
	})
	require.Error(t, err, "invalid GitHub event should return an error before any store calls")
	require.Empty(t, store.calls, "store should not be consulted when the event is invalid")
}

func TestGitHubEventTriggerService_StoreErrorPropagates(t *testing.T) {
	t.Parallel()

	storeErr := errors.New("db unavailable")
	store := &fakeGitHubAutomationStore{err: storeErr}
	runs := &fakeGitHubAutomationRunStore{}
	jobs := &fakeGitHubAutomationJobStore{}
	service := newTestService(store, runs, jobs)

	err := service.TriggerGitHubEvent(context.Background(), GitHubEventTriggerRequest{
		OrgID: uuid.New(), RepositoryID: uuid.New(),
		Event: models.AutomationGitHubEventIssueCommentCreated,
	})
	require.Error(t, err, "store error should propagate from TriggerGitHubEvent")
	require.ErrorContains(t, err, "list github event automations", "error should wrap the store failure with context")
}

func TestGitHubEventTriggerService_NilServiceIsNoop(t *testing.T) {
	t.Parallel()

	var service *GitHubEventTriggerService
	err := service.TriggerGitHubEvent(context.Background(), GitHubEventTriggerRequest{
		OrgID: uuid.New(), RepositoryID: uuid.New(),
		Event: models.AutomationGitHubEventPullRequestOpened,
	})
	require.NoError(t, err, "nil service should be a no-op and not panic")
}
