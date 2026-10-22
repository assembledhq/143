package automations

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type checkRunHeadResolverFake struct {
	head  PullRequestHeadInfo
	err   error
	calls int
}

func (f *checkRunHeadResolverFake) ResolvePullRequestHead(context.Context, uuid.UUID, uuid.UUID, int) (PullRequestHeadInfo, error) {
	f.calls++
	return f.head, f.err
}

func checkRunFixture(t *testing.T) (*GitHubEventTriggerService, *fakeGitHubAutomationRunStore, *fakeGitHubAutomationJobStore, *checkRunHeadResolverFake, GitHubEventTriggerRequest) {
	t.Helper()
	org, repo := uuid.New(), uuid.New()
	store := &fakeGitHubAutomationStore{automations: []models.Automation{{
		ID: uuid.New(), OrgID: org, RepositoryID: &repo, Name: "First assessment", Goal: "Review the pull request",
		ExecutionMode: models.AutomationExecutionModeSequential, MaxConcurrent: 1, IdentityScope: models.AutomationIdentityScopeOrg,
		GitHubEventFilters: json.RawMessage(`{"check_run":{"name":"review-policy","app_id":123},"labels":["review-ready"]}`),
	}}}
	runs, jobs := &fakeGitHubAutomationRunStore{}, &fakeGitHubAutomationJobStore{}
	service := NewGitHubEventTriggerService(store, runs, jobs, &pagerDutyTxStarterFake{}, zerolog.Nop())
	head := &checkRunHeadResolverFake{head: PullRequestHeadInfo{Number: 5, SHA: "head-one", State: "open", BaseBranch: "main", Labels: []string{"review-ready"}}}
	service.SetCheckRunPullRequestResolver(head)
	req := GitHubEventTriggerRequest{OrgID: org, RepositoryID: repo, Repository: "example/project", PullRequestNumber: 5,
		Event: models.AutomationGitHubEventCheckRunCompleted, HeadSHA: "head-one", CheckName: "review-policy", CheckAppID: 123,
		CheckRunID: 1, CheckConclusion: "neutral", ProviderEventID: "delivery-one"}
	return service, runs, jobs, head, req
}

func TestGitHubCheckRunFilterRejectsBeforeFirstHeadClaim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		change    func(*GitHubEventTriggerService, *checkRunHeadResolverFake, *GitHubEventTriggerRequest)
		wantError bool
	}{
		{name: "unrelated check", change: func(_ *GitHubEventTriggerService, _ *checkRunHeadResolverFake, r *GitHubEventTriggerRequest) {
			r.CheckName = "tests"
		}},
		{name: "case differs", change: func(_ *GitHubEventTriggerService, _ *checkRunHeadResolverFake, r *GitHubEventTriggerRequest) {
			r.CheckName = "Review-policy"
		}},
		{name: "whitespace differs", change: func(_ *GitHubEventTriggerService, _ *checkRunHeadResolverFake, r *GitHubEventTriggerRequest) {
			r.CheckName += " "
		}},
		{name: "wrong app", change: func(_ *GitHubEventTriggerService, _ *checkRunHeadResolverFake, r *GitHubEventTriggerRequest) {
			r.CheckAppID++
		}},
		{name: "check suite", change: func(_ *GitHubEventTriggerService, _ *checkRunHeadResolverFake, r *GitHubEventTriggerRequest) {
			r.Event = models.AutomationGitHubEventCheckSuiteCompleted
		}},
		{name: "old head", change: func(_ *GitHubEventTriggerService, _ *checkRunHeadResolverFake, r *GitHubEventTriggerRequest) {
			r.HeadSHA = "old-head"
		}},
		{name: "missing labels", change: func(_ *GitHubEventTriggerService, h *checkRunHeadResolverFake, r *GitHubEventTriggerRequest) {
			h.head.Labels = nil
			r.LabelsKnown = true
			r.Labels = []string{"review-ready"}
		}},
		{name: "closed", change: func(_ *GitHubEventTriggerService, h *checkRunHeadResolverFake, _ *GitHubEventTriggerRequest) {
			h.head.State = "closed"
		}},
		{name: "draft", change: func(_ *GitHubEventTriggerService, h *checkRunHeadResolverFake, _ *GitHubEventTriggerRequest) {
			h.head.Draft = true
		}},
		{name: "wrong live number", change: func(_ *GitHubEventTriggerService, h *checkRunHeadResolverFake, _ *GitHubEventTriggerRequest) {
			h.head.Number++
		}},
		{name: "live lookup fails", wantError: true, change: func(_ *GitHubEventTriggerService, h *checkRunHeadResolverFake, _ *GitHubEventTriggerRequest) {
			h.err = errors.New("GitHub unavailable")
		}},
		{name: "resolver absent", wantError: true, change: func(s *GitHubEventTriggerService, _ *checkRunHeadResolverFake, _ *GitHubEventTriggerRequest) {
			s.SetCheckRunPullRequestResolver(nil)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, runs, jobs, head, valid := checkRunFixture(t)
			req := valid
			tt.change(s, head, &req)
			err := s.TriggerGitHubEvent(context.Background(), req)
			if tt.wantError {
				require.Error(t, err, "an unavailable live resolver must report failure")
			} else {
				require.NoError(t, err, "a nonmatching event should be ignored")
			}
			require.Empty(t, runs.runs, "a rejected event must not consume a first-head identity")
			require.Empty(t, jobs.jobs, "a rejected event must not enqueue an assessment")
			head.head = PullRequestHeadInfo{Number: valid.PullRequestNumber, SHA: valid.HeadSHA, State: "open", Labels: []string{"review-ready"}}
			head.err = nil
			s.SetCheckRunPullRequestResolver(head)
			require.NoError(t, s.TriggerGitHubEvent(context.Background(), valid), "a later valid event must still trigger this head")
			require.Len(t, runs.runs, 1, "only the later valid event should create a run")
		})
	}
}

func TestGitHubCheckRunFirstHeadPersistsAcrossRepublishAndNewHeads(t *testing.T) {
	t.Parallel()
	s, runs, jobs, head, req := checkRunFixture(t)
	require.NoError(t, s.TriggerGitHubEvent(context.Background(), req), "the matching completed check should trigger")
	initial := runs.runs[0]
	now := time.Now().AddDate(2, 0, 0)
	s.now = func() time.Time { return now }
	req.ProviderEventID, req.CheckRunID, req.CheckConclusion = "owner-republish", 2, "success"
	require.NoError(t, s.TriggerGitHubEvent(context.Background(), req), "same-head republishing should be idempotent without a TTL")
	require.Len(t, runs.runs, 1, "owner approval republishing must not create a second first assessment")
	require.Empty(t, runs.dedupeExpiry, "per-head identity must not use expiring trigger claims")
	req.HeadSHA, head.head.SHA = "head-two", "head-two"
	req.ProviderEventID = "new-head"
	require.NoError(t, s.TriggerGitHubEvent(context.Background(), req), "a new head should create one new assessment")
	req.PullRequestNumber, head.head.Number = 6, 6
	req.ProviderEventID = "other-pr"
	require.NoError(t, s.TriggerGitHubEvent(context.Background(), req), "different PRs sharing a head have separate assessments")
	require.Len(t, jobs.jobs, 3, "only the initial, new-head, and other-PR assessments should queue")
	config := map[string]any{}
	require.NoError(t, json.Unmarshal(initial.ConfigSnapshot, &config), "config provenance should decode")
	github := config["github"].(map[string]any)
	require.Equal(t, "delivery-one:pr:5", github["provider_event_id"], "original delivery provenance must not be replaced by the dedupe identity")
	require.Equal(t, map[string]any{"name": "review-policy", "app_id": float64(123), "id": float64(1), "head_sha": "head-one", "conclusion": "neutral"}, github["check_run"], "check source and head must survive in provenance")
	trigger := map[string]any{}
	require.NoError(t, json.Unmarshal(initial.TriggerContext, &trigger), "trigger provenance should decode")
	require.Equal(t, "delivery-one:pr:5", trigger["provider_event_id"], "trigger context should preserve the webhook delivery")
	require.Equal(t, *initial.ProviderEventID, trigger["first_head_key"], "durable identity should be explicit separately from the delivery")
}

func TestGitHubCheckRunLiveFailureDoesNotSuppressLegacySibling(t *testing.T) {
	t.Parallel()
	s, runs, _, head, req := checkRunFixture(t)
	store := s.automations.(*fakeGitHubAutomationStore)
	legacy := store.automations[0]
	legacy.ID, legacy.GitHubEventFilters = uuid.New(), nil
	store.automations = append(store.automations, legacy)
	head.err = errors.New("GitHub unavailable")
	require.Error(t, s.TriggerGitHubEvent(context.Background(), req), "filtered live lookup failure should be reported")
	require.Len(t, runs.runs, 1, "a legacy unfiltered automation should retain existing delivery behavior")
	require.Equal(t, legacy.ID, runs.runs[0].AutomationID, "only the legacy sibling should run")
}

func TestGitHubCheckRunDiscoveryFailurePreservesRedelivery(t *testing.T) {
	t.Parallel()
	service, runs, jobs, _, req := checkRunFixture(t)
	store := service.automations.(*fakeGitHubAutomationStore)
	store.err = errors.New("database unavailable")
	require.ErrorIs(t, service.TriggerGitHubEvent(context.Background(), req), ErrCheckRunTrigger, "unknown check configuration must preserve failed delivery")
	require.Empty(t, runs.runs, "discovery failure cannot consume a head claim")
	require.Empty(t, jobs.jobs, "discovery failure cannot queue work")
	store.err = nil
	require.NoError(t, service.TriggerGitHubEvent(context.Background(), req), "redelivery after discovery recovery should succeed")
	require.Len(t, runs.runs, 1, "recovered delivery should claim the head once")
}

func TestGitHubCheckRunMalformedSiblingCannotSuppressBoundAutomation(t *testing.T) {
	t.Parallel()
	service, runs, jobs, _, req := checkRunFixture(t)
	store := service.automations.(*fakeGitHubAutomationStore)
	bound := store.automations[0]
	malformed := bound
	malformed.ID, malformed.GitHubEventFilters = uuid.New(), json.RawMessage(`{`)
	store.automations = []models.Automation{malformed, bound}
	require.ErrorIs(t, service.TriggerGitHubEvent(context.Background(), req), ErrCheckRunTrigger, "unknown malformed configuration should preserve retryability")
	require.Len(t, runs.runs, 1, "malformed sibling must not suppress the matching bound automation")
	require.Equal(t, bound.ID, runs.runs[0].AutomationID, "only the correctly configured sibling should claim")
	require.Len(t, jobs.jobs, 1, "the bound sibling should queue once")
	require.ErrorIs(t, service.TriggerGitHubEvent(context.Background(), req), ErrCheckRunTrigger, "unrepaired configuration remains a failed delivery")
	require.Len(t, jobs.jobs, 1, "partial success must remain safe on redelivery")
}

type failingSiblingRunStore struct {
	*fakeGitHubAutomationRunStore
	failedAutomation uuid.UUID
}

func (f *failingSiblingRunStore) CreateRunInTx(ctx context.Context, tx pgx.Tx, run *models.AutomationRun) (bool, error) {
	if run.AutomationID == f.failedAutomation {
		return false, errors.New("legacy run unavailable")
	}
	return f.fakeGitHubAutomationRunStore.CreateRunInTx(ctx, tx, run)
}
func TestGitHubCheckRunLegacyExecutionFailureCannotSuppressBoundAutomation(t *testing.T) {
	t.Parallel()
	service, runs, jobs, _, req := checkRunFixture(t)
	store := service.automations.(*fakeGitHubAutomationStore)
	bound := store.automations[0]
	legacy := bound
	legacy.ID, legacy.GitHubEventFilters = uuid.New(), nil
	store.automations = []models.Automation{legacy, bound}
	service.runs = &failingSiblingRunStore{fakeGitHubAutomationRunStore: runs, failedAutomation: legacy.ID}
	err := service.TriggerGitHubEvent(context.Background(), req)
	require.Error(t, err, "legacy execution failure should remain observable")
	require.NotErrorIs(t, err, ErrCheckRunTrigger, "legacy execution failure retains best-effort HTTP handling")
	require.Len(t, jobs.jobs, 1, "legacy failure must not suppress the matching bound automation")
	require.Equal(t, bound.ID, runs.runs[0].AutomationID, "the bound automation should own the successful run")
}
