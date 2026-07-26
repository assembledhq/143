package automations

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
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
	runs         []models.AutomationRun
	err          error
	claimedKeys  map[string]struct{}
	dedupeClaims []string
}

func (f *fakeGitHubAutomationRunStore) CreateRunInTx(_ context.Context, _ pgx.Tx, run *models.AutomationRun) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	run.ID = uuid.New()
	f.runs = append(f.runs, *run)
	return true, nil
}

func (f *fakeGitHubAutomationRunStore) ClaimTriggerDedupe(_ context.Context, _ uuid.UUID, automationID uuid.UUID, dedupeKey string, _ time.Time) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	key := automationID.String() + ":" + dedupeKey
	f.dedupeClaims = append(f.dedupeClaims, key)
	if f.claimedKeys == nil {
		f.claimedKeys = map[string]struct{}{}
	}
	if _, ok := f.claimedKeys[key]; ok {
		return false, nil
	}
	f.claimedKeys[key] = struct{}{}
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

func (f *fakeGitHubAutomationJobStore) EnqueueInTx(_ context.Context, _ pgx.Tx, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
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
	return NewGitHubEventTriggerService(store, runs, jobs, &pagerDutyTxStarterFake{}, zerolog.Nop())
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
	service := NewGitHubEventTriggerService(store, runs, jobs, &pagerDutyTxStarterFake{}, zerolog.Nop())

	err := service.TriggerGitHubEvent(context.Background(), GitHubEventTriggerRequest{
		OrgID: orgID, RepositoryID: repoID, Event: models.AutomationGitHubEventPullRequestOpened,
		Repository: "acme/api", PullRequestNumber: 42,
		PullRequestTitle: "Improve checkout", HeadSHA: "abc123",
		Actor: "octocat", ActorType: "User", Body: "please review",
		ProviderEventID: "delivery-123", EventID: "pull_request:opened:42",
	})
	require.NoError(t, err, "triggering a GitHub event should succeed")
	require.Equal(t, []fakeListGitHubEventCall{{
		orgID: orgID, repositoryID: repoID, event: models.AutomationGitHubEventPullRequestOpened,
	}}, store.calls, "service should list automations by org, repository, and event")
	require.Len(t, runs.runs, 1, "matching automation should create one run")
	require.Equal(t, models.AutomationTriggeredByGitHub, runs.runs[0].TriggeredBy, "run should record GitHub as the trigger source")
	require.NotNil(t, runs.runs[0].Provider, "run should record the trigger provider")
	require.Equal(t, models.AutomationEventProviderGitHub, *runs.runs[0].Provider, "run should identify GitHub as the provider")
	require.NotNil(t, runs.runs[0].ProviderEventID, "run should preserve the GitHub delivery id")
	require.Equal(t, "delivery-123:pr:42", *runs.runs[0].ProviderEventID, "delivery id should be scoped to the target PR")
	require.Contains(t, runs.runs[0].GoalSnapshot, "Run a review", "goal snapshot should include the automation goal")
	require.Contains(t, runs.runs[0].GoalSnapshot, "PR #42", "goal snapshot should include pull request context")
	require.Contains(t, runs.runs[0].GoalSnapshot, "Improve checkout", "goal snapshot should include the pull request title")
	require.Contains(t, runs.runs[0].GoalSnapshot, "abc123", "goal snapshot should include the evaluated head SHA")
	require.Len(t, jobs.jobs, 1, "matching automation should enqueue one worker job")
	require.Equal(t, models.JobTypeAutomationRun, jobs.jobs[0].jobType, "job type should dispatch the automation worker")
	require.Equal(t, []uuid.UUID{jobID}, jobs.notified, "created job should be notified")

	var config map[string]any
	require.NoError(t, json.Unmarshal(runs.runs[0].ConfigSnapshot, &config), "config snapshot should be valid JSON")
	require.Equal(t, string(models.AutomationIdentityScopeOrg), config["identity_scope"], "config snapshot should preserve automation identity scope")
	require.Equal(t, string(models.AutomationGitHubEventPullRequestOpened), config["github_event"], "config snapshot should include the GitHub event")
	require.Equal(t, "PR opened", config["github_trigger"], "config snapshot should include a product-level trigger label")
	github, ok := config["github"].(map[string]any)
	require.True(t, ok, "config snapshot should include typed GitHub context")
	require.Equal(t, "https://github.com/acme/api/pull/42", github["pull_request_url"], "missing webhook URL should be normalized from repository and PR number")
	require.Equal(t, "Improve checkout", github["pull_request_title"], "config snapshot should include the PR title")
	require.Equal(t, "abc123", github["head_sha"], "config snapshot should include the evaluated head SHA")
	require.Equal(t, "delivery-123:pr:42", github["provider_event_id"], "config snapshot should include the scoped delivery id")

	var triggerContext map[string]any
	require.NoError(t, json.Unmarshal(runs.runs[0].TriggerContext, &triggerContext), "trigger context should be valid JSON")
	require.Equal(t, "github", triggerContext["provider"], "trigger context should identify GitHub")
	require.Equal(t, "delivery-123:pr:42", triggerContext["provider_event_id"], "trigger context should include the scoped delivery id")
}

func TestNormalizeGitHubEventTriggerRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		request  GitHubEventTriggerRequest
		expected GitHubEventTriggerRequest
	}{
		{
			name: "fills target URL and infers bot actor type",
			request: GitHubEventTriggerRequest{
				Repository: " acme/api ", PullRequestNumber: 42,
				Actor: "dependabot[bot]", ProviderEventID: " delivery-1 ",
			},
			expected: GitHubEventTriggerRequest{
				Repository: "acme/api", PullRequestNumber: 42,
				PullRequestURL: "https://github.com/acme/api/pull/42",
				Actor:          "dependabot[bot]", ActorType: "Bot", ProviderEventID: "delivery-1:pr:42",
			},
		},
		{
			name: "does not append PR suffix twice",
			request: GitHubEventTriggerRequest{
				Repository: "acme/api", PullRequestNumber: 42,
				ProviderEventID: "delivery-1:pr:42",
			},
			expected: GitHubEventTriggerRequest{
				Repository: "acme/api", PullRequestNumber: 42,
				PullRequestURL:  "https://github.com/acme/api/pull/42",
				ProviderEventID: "delivery-1:pr:42",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, normalizeGitHubEventTriggerRequest(tt.request), "normalization should produce stable trigger context")
		})
	}
}

func TestGitHubEventTriggerService_DedupesFeedbackByReviewGroup(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	automationID := uuid.New()
	store := &fakeGitHubAutomationStore{automations: []models.Automation{{
		ID: automationID, OrgID: orgID, RepositoryID: &repoID, Name: "Review PR", Goal: "Run a review",
		ExecutionMode: models.AutomationExecutionModeSequential, MaxConcurrent: 1, BaseBranch: "main",
		IdentityScope: models.AutomationIdentityScopeOrg,
	}}}
	runs := &fakeGitHubAutomationRunStore{}
	jobs := &fakeGitHubAutomationJobStore{}
	service := NewGitHubEventTriggerService(store, runs, jobs, &pagerDutyTxStarterFake{}, zerolog.Nop())

	req := GitHubEventTriggerRequest{
		OrgID: orgID, RepositoryID: repoID, Event: models.AutomationGitHubEventPullRequestReviewCommentCreated,
		Repository: "acme/api", PullRequestNumber: 42, Actor: "reviewer", Body: "line comment",
		EventID: "review-comment:1001", DedupeGroupID: "review:9001",
	}
	require.NoError(t, service.TriggerGitHubEvent(context.Background(), req), "first feedback event should trigger a run")
	req.Event = models.AutomationGitHubEventPullRequestReviewSubmitted
	req.Body = "submitted review"
	req.EventID = "review:9001"
	req.DedupeGroupID = "review:9001"
	require.NoError(t, service.TriggerGitHubEvent(context.Background(), req), "second feedback event in burst should be debounced")
	require.Len(t, runs.runs, 1, "feedback events in the same GitHub review group should create one run inside the debounce window")
	require.Contains(t, runs.runs[0].GoalSnapshot, "Trigger: New PR feedback", "goal snapshot should use the product-level trigger label")
}

func TestGitHubEventTriggerService_DoesNotDedupeDistinctFeedbackEvents(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	automationID := uuid.New()
	store := &fakeGitHubAutomationStore{automations: []models.Automation{{
		ID: automationID, OrgID: orgID, RepositoryID: &repoID, Name: "Review PR", Goal: "Run a review",
		ExecutionMode: models.AutomationExecutionModeSequential, MaxConcurrent: 1, BaseBranch: "main",
		IdentityScope: models.AutomationIdentityScopeOrg,
	}}}
	runs := &fakeGitHubAutomationRunStore{}
	jobs := &fakeGitHubAutomationJobStore{}
	service := NewGitHubEventTriggerService(store, runs, jobs, &pagerDutyTxStarterFake{}, zerolog.Nop())

	req := GitHubEventTriggerRequest{
		OrgID: orgID, RepositoryID: repoID, Event: models.AutomationGitHubEventIssueCommentCreated,
		Repository: "acme/api", PullRequestNumber: 42, Actor: "reviewer", Body: "first",
		EventID: "issue-comment:1001",
	}
	require.NoError(t, service.TriggerGitHubEvent(context.Background(), req), "first comment should trigger a run")
	req.Body = "second"
	req.EventID = "issue-comment:1002"
	require.NoError(t, service.TriggerGitHubEvent(context.Background(), req), "distinct comment should trigger another run")
	require.Len(t, runs.runs, 2, "distinct comment IDs should not be collapsed by the feedback debounce")
}

func TestGitHubEventTriggerService_AppliesGitHubEventFilters(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	filters := json.RawMessage(`{"base_branches":["main"],"authors":["octocat"],"paths":["src/"],"feedback_types":["Inline review comment"],"review_states":["commented"]}`)
	store := &fakeGitHubAutomationStore{automations: []models.Automation{{
		ID: uuid.New(), OrgID: orgID, RepositoryID: &repoID, Name: "Review PR", Goal: "Run a review",
		ExecutionMode: models.AutomationExecutionModeSequential, MaxConcurrent: 1, BaseBranch: "main",
		IdentityScope: models.AutomationIdentityScopeOrg, GitHubEventFilters: filters,
	}}}
	runs := &fakeGitHubAutomationRunStore{}
	jobs := &fakeGitHubAutomationJobStore{}
	service := NewGitHubEventTriggerService(store, runs, jobs, &pagerDutyTxStarterFake{}, zerolog.Nop())

	err := service.TriggerGitHubEvent(context.Background(), GitHubEventTriggerRequest{
		OrgID: orgID, RepositoryID: repoID, Event: models.AutomationGitHubEventPullRequestReviewCommentCreated,
		Repository: "acme/api", PullRequestNumber: 42, Actor: "octocat", Body: "line comment",
		EventID: "review-comment:1001", BaseBranch: "main", Path: "src/api/handler.go", ReviewState: "commented",
	})
	require.NoError(t, err, "matching filters should not error")
	require.Len(t, runs.runs, 1, "matching filters should allow a run")

	err = service.TriggerGitHubEvent(context.Background(), GitHubEventTriggerRequest{
		OrgID: orgID, RepositoryID: repoID, Event: models.AutomationGitHubEventPullRequestReviewCommentCreated,
		Repository: "acme/api", PullRequestNumber: 42, Actor: "octocat", Body: "line comment",
		EventID: "review-comment:1002", BaseBranch: "release", Path: "src/api/handler.go", ReviewState: "commented",
	})
	require.NoError(t, err, "nonmatching filters should not error")
	require.Len(t, runs.runs, 1, "nonmatching filters should not create a run")
}

func TestGitHubEventTriggerService_PathFilterSkipsEventsWithNoPath(t *testing.T) {
	t.Parallel()

	// PR review submissions have no file path. A paths filter must not block
	// them — the filter should only fire when the event carries a path.
	orgID := uuid.New()
	repoID := uuid.New()
	filters := json.RawMessage(`{"paths":["src/"]}`)
	store := &fakeGitHubAutomationStore{automations: []models.Automation{{
		ID: uuid.New(), OrgID: orgID, RepositoryID: &repoID, Name: "Review PR", Goal: "Run a review",
		ExecutionMode: models.AutomationExecutionModeSequential, MaxConcurrent: 1,
		IdentityScope: models.AutomationIdentityScopeOrg, GitHubEventFilters: filters,
	}}}
	runs := &fakeGitHubAutomationRunStore{}
	jobs := &fakeGitHubAutomationJobStore{}
	service := NewGitHubEventTriggerService(store, runs, jobs, &pagerDutyTxStarterFake{}, zerolog.Nop())

	err := service.TriggerGitHubEvent(context.Background(), GitHubEventTriggerRequest{
		OrgID: orgID, RepositoryID: repoID, Event: models.AutomationGitHubEventPullRequestReviewSubmitted,
		Repository: "acme/api", PullRequestNumber: 42, Actor: "reviewer", Body: "looks good",
		EventID: "review:100",
		// Path intentionally omitted — review submissions are not file-specific.
	})
	require.NoError(t, err)
	require.Len(t, runs.runs, 1, "review submission with no path should not be suppressed by a paths filter")
}

func TestGitHubEventTriggerService_FeedbackTypeFilterSkipsNonFeedbackEvents(t *testing.T) {
	t.Parallel()

	// An automation with both PR-opened and PR-feedback triggers may carry a
	// feedback_types filter. That filter must not block PR-opened events, which
	// have no feedback type.
	orgID := uuid.New()
	repoID := uuid.New()
	filters := json.RawMessage(`{"feedback_types":["Inline review comment"]}`)
	store := &fakeGitHubAutomationStore{automations: []models.Automation{{
		ID: uuid.New(), OrgID: orgID, RepositoryID: &repoID, Name: "Dual trigger", Goal: "Run on PR events",
		ExecutionMode: models.AutomationExecutionModeSequential, MaxConcurrent: 1,
		IdentityScope: models.AutomationIdentityScopeOrg, GitHubEventFilters: filters,
	}}}
	runs := &fakeGitHubAutomationRunStore{}
	jobs := &fakeGitHubAutomationJobStore{}
	service := NewGitHubEventTriggerService(store, runs, jobs, &pagerDutyTxStarterFake{}, zerolog.Nop())

	err := service.TriggerGitHubEvent(context.Background(), GitHubEventTriggerRequest{
		OrgID: orgID, RepositoryID: repoID, Event: models.AutomationGitHubEventPullRequestOpened,
		Repository: "acme/api", PullRequestNumber: 42, Actor: "dev", Body: "new PR",
		EventID: "pull_request:opened:42",
	})
	require.NoError(t, err)
	require.Len(t, runs.runs, 1, "PR opened event should not be suppressed by a feedback_types filter")
}

func TestGitHubEventTriggerService_ReviewStateFilterSkipsEventsWithNoState(t *testing.T) {
	t.Parallel()

	// Inline review comments have no review state. A review_states filter must
	// not block them — the filter only applies when the event carries a state.
	orgID := uuid.New()
	repoID := uuid.New()
	filters := json.RawMessage(`{"review_states":["changes_requested"]}`)
	store := &fakeGitHubAutomationStore{automations: []models.Automation{{
		ID: uuid.New(), OrgID: orgID, RepositoryID: &repoID, Name: "Review PR", Goal: "Run a review",
		ExecutionMode: models.AutomationExecutionModeSequential, MaxConcurrent: 1,
		IdentityScope: models.AutomationIdentityScopeOrg, GitHubEventFilters: filters,
	}}}
	runs := &fakeGitHubAutomationRunStore{}
	jobs := &fakeGitHubAutomationJobStore{}
	service := NewGitHubEventTriggerService(store, runs, jobs, &pagerDutyTxStarterFake{}, zerolog.Nop())

	err := service.TriggerGitHubEvent(context.Background(), GitHubEventTriggerRequest{
		OrgID: orgID, RepositoryID: repoID, Event: models.AutomationGitHubEventPullRequestReviewCommentCreated,
		Repository: "acme/api", PullRequestNumber: 42, Actor: "reviewer", Body: "nit: style",
		EventID: "review_comment:200", Path: "src/main.go",
		// ReviewState intentionally omitted — inline comments carry no review state.
	})
	require.NoError(t, err)
	require.Len(t, runs.runs, 1, "inline review comment with no review state should not be suppressed by a review_states filter")
}

func TestGitHubEventTriggerService_BaseBranchFilterAllowsFeedbackWithNoBaseBranch(t *testing.T) {
	t.Parallel()

	// IssueCommentEvent carries no base branch information from GitHub webhooks.
	// An automation with a base_branches filter must not silently block such events;
	// the filter is skipped when BaseBranch is empty.
	orgID := uuid.New()
	repoID := uuid.New()
	filters := json.RawMessage(`{"base_branches":["main"]}`)
	store := &fakeGitHubAutomationStore{automations: []models.Automation{{
		ID: uuid.New(), OrgID: orgID, RepositoryID: &repoID, Name: "Review PR", Goal: "Run a review",
		ExecutionMode: models.AutomationExecutionModeSequential, MaxConcurrent: 1,
		IdentityScope: models.AutomationIdentityScopeOrg, GitHubEventFilters: filters,
	}}}
	runs := &fakeGitHubAutomationRunStore{}
	jobs := &fakeGitHubAutomationJobStore{}
	service := NewGitHubEventTriggerService(store, runs, jobs, &pagerDutyTxStarterFake{}, zerolog.Nop())

	err := service.TriggerGitHubEvent(context.Background(), GitHubEventTriggerRequest{
		OrgID: orgID, RepositoryID: repoID, Event: models.AutomationGitHubEventIssueCommentCreated,
		Repository: "acme/api", PullRequestNumber: 42, Actor: "octocat", Body: "comment",
		EventID: "issue-comment:5000",
		// BaseBranch intentionally omitted — IssueCommentEvent has no base branch.
	})
	require.NoError(t, err)
	require.Len(t, runs.runs, 1, "issue comment with no base branch should not be suppressed by a base_branches filter")
}

func TestMatchesPathFilter_SegmentBoundary(t *testing.T) {
	t.Parallel()

	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		// directory prefix (trailing slash)
		{"src/", "src/main.go", true},
		{"src/", "mysrc/main.go", false},
		// path prefix without trailing slash
		{"src", "src/main.go", true},
		{"src", "srcbar/main.go", false},
		// middle segment
		{"api", "internal/api/handler.go", true},
		{"api", "internal/myapi/handler.go", false},
		// filename suffix
		{"handler.go", "internal/api/handler.go", true},
		{"handler.go", "internal/api/other_handler.go", false},
		// exact match
		{"main.go", "main.go", true},
		// sub-path suffix
		{"api/handler.go", "internal/api/handler.go", true},
	}
	for _, tc := range cases {
		got := matchesPathFilter([]string{tc.pattern}, tc.path)
		if got != tc.want {
			t.Errorf("matchesPathFilter(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
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

func TestGitHubEventTriggerService_RollsBackRunWhenEnqueueFails(t *testing.T) {
	t.Parallel()

	txStarter, err := pgxmock.NewPool()
	require.NoError(t, err, "transaction mock should initialize")
	defer txStarter.Close()
	txStarter.ExpectBegin()
	txStarter.ExpectRollback()

	orgID := uuid.New()
	repoID := uuid.New()
	store := &fakeGitHubAutomationStore{automations: []models.Automation{{
		ID: uuid.New(), OrgID: orgID, RepositoryID: &repoID, Name: "Review PR", Goal: "Run a review",
		ExecutionMode: models.AutomationExecutionModeSequential, MaxConcurrent: 1,
		IdentityScope: models.AutomationIdentityScopeOrg,
	}}}
	runs := &fakeGitHubAutomationRunStore{}
	jobs := &fakeGitHubAutomationJobStore{err: errors.New("queue unavailable")}
	service := NewGitHubEventTriggerService(store, runs, jobs, txStarter, zerolog.Nop())

	err = service.TriggerGitHubEvent(context.Background(), GitHubEventTriggerRequest{
		OrgID: orgID, RepositoryID: repoID, Event: models.AutomationGitHubEventPullRequestOpened,
		Repository: "acme/api", PullRequestNumber: 42, ProviderEventID: "delivery-123",
	})

	require.ErrorContains(t, err, "enqueue github-triggered automation run", "enqueue failure should be returned to the webhook path")
	require.Empty(t, jobs.notified, "a rolled-back job should not notify workers")
	require.NoError(t, txStarter.ExpectationsWereMet(), "run insert and job enqueue should roll back together")
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

type fakeGitHubLabelResolver struct {
	labels []string
	err    error
	calls  int
}

func (f *fakeGitHubLabelResolver) ResolvePullRequestLabels(_ context.Context, _, _ uuid.UUID, _ int) ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.labels, nil
}

func newLabelFilterAutomation(orgID, repoID uuid.UUID, filters json.RawMessage) models.Automation {
	return models.Automation{
		ID: uuid.New(), OrgID: orgID, RepositoryID: &repoID, Name: "Frontend review", Goal: "Review frontend PRs",
		ExecutionMode: models.AutomationExecutionModeSequential, MaxConcurrent: 1,
		IdentityScope: models.AutomationIdentityScopeOrg, GitHubEventFilters: filters,
	}
}

func TestGitHubEventTriggerService_LabelFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		filters     json.RawMessage
		labels      []string
		labelsKnown bool
		expectRuns  int
	}{
		{
			name:        "runs when the pull request carries the configured label",
			filters:     json.RawMessage(`{"labels":["frontend"]}`),
			labels:      []string{"frontend", "needs-design"},
			labelsKnown: true,
			expectRuns:  1,
		},
		{
			name:        "matches labels case-insensitively",
			filters:     json.RawMessage(`{"labels":["Frontend"]}`),
			labels:      []string{"frontend"},
			labelsKnown: true,
			expectRuns:  1,
		},
		{
			name:        "runs when any configured label matches",
			filters:     json.RawMessage(`{"labels":["backend","frontend"]}`),
			labels:      []string{"frontend"},
			labelsKnown: true,
			expectRuns:  1,
		},
		{
			name:        "skips when the pull request carries no configured label",
			filters:     json.RawMessage(`{"labels":["frontend"]}`),
			labels:      []string{"backend"},
			labelsKnown: true,
			expectRuns:  0,
		},
		{
			name:        "skips an unlabelled pull request",
			filters:     json.RawMessage(`{"labels":["frontend"]}`),
			labels:      []string{},
			labelsKnown: true,
			expectRuns:  0,
		},
		{
			name:        "skips when labels could not be determined",
			filters:     json.RawMessage(`{"labels":["frontend"]}`),
			labels:      nil,
			labelsKnown: false,
			expectRuns:  0,
		},
		{
			name:        "labels on the event do not block an automation without a label filter",
			filters:     json.RawMessage(`{"base_branches":["main"]}`),
			labels:      []string{"frontend"},
			labelsKnown: true,
			expectRuns:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			repoID := uuid.New()
			store := &fakeGitHubAutomationStore{automations: []models.Automation{
				newLabelFilterAutomation(orgID, repoID, tt.filters),
			}}
			runs := &fakeGitHubAutomationRunStore{}
			jobs := &fakeGitHubAutomationJobStore{}
			service := newTestService(store, runs, jobs)

			err := service.TriggerGitHubEvent(context.Background(), GitHubEventTriggerRequest{
				OrgID: orgID, RepositoryID: repoID,
				Event:      models.AutomationGitHubEventPullRequestReadyForReview,
				Repository: "acme/api", PullRequestNumber: 42, Actor: "octocat",
				EventID: "pull_request:ready_for_review:42", BaseBranch: "main",
				Labels: tt.labels, LabelsKnown: tt.labelsKnown,
			})
			require.NoError(t, err, "label filtering should not error")
			require.Len(t, runs.runs, tt.expectRuns, "label filter should decide whether the automation runs")
		})
	}
}

func TestGitHubEventTriggerService_ResolvesLabelsWhenPayloadOmitsThem(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		filters      json.RawMessage
		resolver     *fakeGitHubLabelResolver
		expectCalls  int
		expectRuns   int
		expectLabels []string
	}{
		{
			name:         "resolves labels for a check event when an automation filters on labels",
			filters:      json.RawMessage(`{"labels":["frontend"]}`),
			resolver:     &fakeGitHubLabelResolver{labels: []string{"frontend"}},
			expectCalls:  1,
			expectRuns:   1,
			expectLabels: []string{"frontend"},
		},
		{
			name:        "does not call the GitHub API when no automation filters on labels",
			filters:     json.RawMessage(`{"base_branches":["main"]}`),
			resolver:    &fakeGitHubLabelResolver{labels: []string{"frontend"}},
			expectCalls: 0,
			expectRuns:  1,
		},
		{
			name:        "skips the run when label resolution fails",
			filters:     json.RawMessage(`{"labels":["frontend"]}`),
			resolver:    &fakeGitHubLabelResolver{err: errors.New("github unavailable")},
			expectCalls: 1,
			expectRuns:  0,
		},
		{
			name:        "skips the run when the resolved labels do not match",
			filters:     json.RawMessage(`{"labels":["frontend"]}`),
			resolver:    &fakeGitHubLabelResolver{labels: []string{"backend"}},
			expectCalls: 1,
			expectRuns:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			repoID := uuid.New()
			store := &fakeGitHubAutomationStore{automations: []models.Automation{
				newLabelFilterAutomation(orgID, repoID, tt.filters),
			}}
			runs := &fakeGitHubAutomationRunStore{}
			jobs := &fakeGitHubAutomationJobStore{}
			service := newTestService(store, runs, jobs)
			service.SetLabelResolver(tt.resolver)

			err := service.TriggerGitHubEvent(context.Background(), GitHubEventTriggerRequest{
				OrgID: orgID, RepositoryID: repoID,
				Event:      models.AutomationGitHubEventCheckSuiteCompleted,
				Repository: "acme/api", PullRequestNumber: 42, Actor: "github",
				BaseBranch: "main", ProviderEventID: "delivery-1",
				// Labels intentionally omitted — check_suite payloads carry none.
			})
			require.NoError(t, err, "label resolution should not error the trigger")
			require.Equal(t, tt.expectCalls, tt.resolver.calls, "label resolver should only be consulted when a label filter exists")
			require.Len(t, runs.runs, tt.expectRuns, "resolved labels should decide whether the automation runs")
			if tt.expectRuns > 0 && len(tt.expectLabels) > 0 {
				var snapshot struct {
					GitHub struct {
						Labels []string `json:"labels"`
					} `json:"github"`
				}
				require.NoError(t, json.Unmarshal(runs.runs[0].ConfigSnapshot, &snapshot), "config snapshot should be valid JSON")
				require.Equal(t, tt.expectLabels, snapshot.GitHub.Labels, "resolved labels should be recorded on the run snapshot")
			}
		})
	}
}

func TestGitHubEventTriggerService_ReadyForReviewSnapshot(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	store := &fakeGitHubAutomationStore{automations: []models.Automation{
		newLabelFilterAutomation(orgID, repoID, json.RawMessage(`{}`)),
	}}
	runs := &fakeGitHubAutomationRunStore{}
	jobs := &fakeGitHubAutomationJobStore{}
	service := newTestService(store, runs, jobs)

	err := service.TriggerGitHubEvent(context.Background(), GitHubEventTriggerRequest{
		OrgID: orgID, RepositoryID: repoID,
		Event:      models.AutomationGitHubEventPullRequestReadyForReview,
		Repository: "acme/api", PullRequestNumber: 42, Actor: "octocat",
		EventID: "pull_request:ready_for_review:42",
		Labels:  []string{"frontend", " "}, LabelsKnown: true,
	})
	require.NoError(t, err, "ready-for-review trigger should not error")
	require.Len(t, runs.runs, 1, "ready-for-review event should create a run")

	var snapshot struct {
		Trigger string `json:"github_trigger"`
		Event   string `json:"github_event"`
		GitHub  struct {
			Labels []string `json:"labels"`
		} `json:"github"`
	}
	require.NoError(t, json.Unmarshal(runs.runs[0].ConfigSnapshot, &snapshot), "config snapshot should be valid JSON")
	require.Equal(t, "PR ready for review", snapshot.Trigger, "ready-for-review should get its own human-readable trigger label")
	require.Equal(t, "github.pull_request.ready_for_review", snapshot.Event, "snapshot should record the raw event")
	require.Equal(t, []string{"frontend"}, snapshot.GitHub.Labels, "blank label names should be dropped from the snapshot")
	require.Contains(t, runs.runs[0].GoalSnapshot, "- Labels: frontend", "goal snapshot should describe the PR labels")
}
