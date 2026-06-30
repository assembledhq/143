package github

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

func TestPRServiceCancelMergeWhenReadyOffIsNoop(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	prID := uuid.New()
	now := time.Now().UTC()
	row := newPRTestRow(prID, nil, orgID, "assembledhq/143", now, nil)
	row[21] = models.PullRequestMergeWhenReadyStateOff

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(row...))

	service := &PRService{
		pullRequests: db.NewPullRequestStore(mock),
		logger:       zerolog.New(io.Discard),
	}

	status, err := service.CancelMergeWhenReady(context.Background(), orgID, prID, uuid.New())
	require.NoError(t, err, "CancelMergeWhenReady should treat off state as a no-op")
	require.Equal(t, models.PullRequestMergeWhenReadyStateOff, status.State, "CancelMergeWhenReady should preserve off state")
	require.NoError(t, mock.ExpectationsWereMet(), "cancel off should only load the pull request")
}

func TestPRServiceProcessMergeWhenReadyRecoversStaleMergingIntent(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	prID := uuid.New()
	userID := uuid.New()
	now := time.Now().UTC()
	staleUpdatedAt := now.Add(-mergeWhenReadyMergingStaleAfter - time.Minute)
	row := newPRTestRow(prID, nil, orgID, "assembledhq/143", now, nil)
	row[8] = models.PullRequestStatusClosed
	row[21] = models.PullRequestMergeWhenReadyStateMerging
	row[22] = &userID
	row[27] = &staleUpdatedAt

	mock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(row...))
	mock.ExpectExec("UPDATE pull_requests[\\s\\S]*merge_when_ready_state = @state[\\s\\S]*merge_when_ready_error = @reason").
		WithArgs(pgx.NamedArgs{
			"id":     prID,
			"org_id": orgID,
			"state":  models.PullRequestMergeWhenReadyStateFailed,
			"reason": "Pull request is no longer open.",
		}).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	service := &PRService{
		pullRequests: db.NewPullRequestStore(mock),
		logger:       zerolog.New(io.Discard),
	}

	err = service.ProcessMergeWhenReady(context.Background(), orgID, prID)
	require.NoError(t, err, "ProcessMergeWhenReady should recover stale merging intents instead of leaving them stuck")
	require.NoError(t, mock.ExpectationsWereMet(), "stale merging recovery should mark closed pull requests failed")
}

func TestPRServiceQueueMergeWhenReadyPreflightsUserRequiredGitHubAuth(t *testing.T) {
	t.Parallel()

	prMock, err := pgxmock.NewPool()
	require.NoError(t, err, "pull request pgxmock pool should initialize")
	defer prMock.Close()
	repoMock, err := pgxmock.NewPool()
	require.NoError(t, err, "repository pgxmock pool should initialize")
	defer repoMock.Close()
	orgMock, err := pgxmock.NewPool()
	require.NoError(t, err, "organization pgxmock pool should initialize")
	defer orgMock.Close()

	orgID := uuid.New()
	prID := uuid.New()
	userID := uuid.New()
	now := time.Now().UTC()
	row := newPRTestRow(prID, nil, orgID, "assembledhq/143", now, nil)
	row[12] = ptrString("head")
	row[14] = ptrString("base")
	row[19] = &now
	row[20] = int64(7)

	summaryJSON, err := json.Marshal(models.PullRequestHealthSummary{
		MergeState:      models.PullRequestMergeStateBlocked,
		ChecksConfirmed: true,
	})
	require.NoError(t, err, "health summary should marshal")

	prMock.ExpectQuery("SELECT .+ FROM pull_requests WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(prTestPullRequestColumns).AddRow(row...))
	prMock.ExpectQuery("SELECT .+ FROM pull_request_health_current WHERE org_id = .+ AND pull_request_id = .+").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID}).
		WillReturnRows(pgxmock.NewRows(prHealthCurrentTestColumns).AddRow(
			prID, orgID, int64(7), "head", "base", summaryJSON, summaryJSON, models.PullRequestHealthEnrichmentStatusNotRequested, nil, now, now,
		))
	orgMock.ExpectQuery("SELECT .+ FROM organizations WHERE id = @id").
		WithArgs(pgx.NamedArgs{"id": orgID}).
		WillReturnRows(pgxmock.NewRows(prTestOrganizationColumns).AddRow(
			orgID, "Acme", []byte(`{"pr_authorship":"user_required"}`), now, now,
		))
	orgMock.ExpectQuery("SELECT .+ FROM organizations WHERE id = @id").
		WithArgs(pgx.NamedArgs{"id": orgID}).
		WillReturnRows(pgxmock.NewRows(prTestOrganizationColumns).AddRow(
			orgID, "Acme", []byte(`{"pr_authorship":"user_required"}`), now, now,
		))
	repoMock.ExpectQuery("SELECT .+ FROM repositories WHERE org_id = .+ AND full_name = .+ AND status = 'active'").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "full_name": "assembledhq/143"}).
		WillReturnRows(pgxmock.NewRows(prTestRepoColumns).AddRow(
			uuid.New(), orgID, uuid.New(), int64(123), "assembledhq/143", "main", false, (*string)(nil), (*string)(nil), "https://github.com/assembledhq/143.git", int64(456), models.RepositoryStatusActive, &now, (*float64)(nil), []byte(`{}`), now, now,
		))

	service := &PRService{
		pullRequests: db.NewPullRequestStore(prMock),
		repos:        db.NewRepositoryStore(repoMock),
		orgs:         db.NewOrganizationStore(orgMock),
		logger:       zerolog.New(io.Discard),
	}

	_, err = service.QueueMergeWhenReady(context.Background(), orgID, prID, userID)
	require.ErrorIs(t, err, ErrGitHubUserAuthRequired, "QueueMergeWhenReady should reject user-required orgs before creating queued intent")
	require.NoError(t, prMock.ExpectationsWereMet(), "queue preflight should not write merge_when_ready state without usable user auth")
	require.NoError(t, repoMock.ExpectationsWereMet(), "queue preflight should load repository for auth resolution")
	require.NoError(t, orgMock.ExpectationsWereMet(), "queue preflight should load org authorship settings")
}

func TestPRServiceEnqueueMergeWhenReadyProcessingIncludesStaleMerging(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		state   models.PullRequestMergeWhenReadyState
		updated *time.Time
		wantJob bool
	}{
		{name: "queued", state: models.PullRequestMergeWhenReadyStateQueued, wantJob: true},
		{name: "stale merging", state: models.PullRequestMergeWhenReadyStateMerging, updated: timePtr(time.Now().Add(-mergeWhenReadyMergingStaleAfter - time.Minute)), wantJob: true},
		{name: "fresh merging", state: models.PullRequestMergeWhenReadyStateMerging, updated: timePtr(time.Now()), wantJob: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should initialize")
			defer mock.Close()

			orgID := uuid.New()
			prID := uuid.New()
			if tt.wantJob {
				mock.ExpectQuery("INSERT INTO jobs").
					WithArgs(pgx.NamedArgs{
						"org_id":     orgID,
						"queue":      "default",
						"job_type":   "merge_pull_request_when_ready",
						"payload":    pgxmock.AnyArg(),
						"priority":   7,
						"dedupe_key": pgxmock.AnyArg(),
					}).
					WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New()))
			}

			service := &PRService{
				jobs:   db.NewJobStore(mock),
				logger: zerolog.New(io.Discard),
			}
			service.enqueueMergeWhenReadyProcessing(context.Background(), models.PullRequest{
				ID:                      prID,
				OrgID:                   orgID,
				MergeWhenReadyState:     tt.state,
				MergeWhenReadyUpdatedAt: tt.updated,
			})
			require.NoError(t, mock.ExpectationsWereMet(), "enqueueMergeWhenReadyProcessing should match expected enqueue behavior")
		})
	}
}

func TestMergeWhenReadyShouldWaitForChecks(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	withinGrace := now.Add(-mergeWhenReadyChecksRegisterGrace / 2)
	pastGrace := now.Add(-mergeWhenReadyChecksRegisterGrace - time.Second)

	tests := []struct {
		name        string
		requestedAt *time.Time
		checks      []models.PullRequestCheckSummary
		want        bool
	}{
		{
			name:        "empty checks within grace waits",
			requestedAt: &withinGrace,
			want:        true,
		},
		{
			name:        "empty checks past grace proceeds (no CI configured)",
			requestedAt: &pastGrace,
			want:        false,
		},
		{
			name:        "registered checks never wait on the empty-checks gate",
			requestedAt: &withinGrace,
			checks:      []models.PullRequestCheckSummary{{Name: "build", Status: models.PullRequestCheckStatusPending}},
			want:        false,
		},
		{
			name:        "missing requested-at proceeds",
			requestedAt: nil,
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pr := models.PullRequest{MergeWhenReadyRequestedAt: tt.requestedAt}
			health := &models.PullRequestHealthResponse{Checks: tt.checks}
			require.Equal(t, tt.want, mergeWhenReadyShouldWaitForChecks(pr, health, now))
		})
	}
}

func TestMergeBlockIsTransient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		health *models.PullRequestHealthResponse
		want   bool
	}{
		{
			name:   "pending checks are transient",
			health: &models.PullRequestHealthResponse{MergeState: models.PullRequestMergeStateBlocked, Checks: []models.PullRequestCheckSummary{{Name: "test", Status: models.PullRequestCheckStatusPending}}},
			want:   true,
		},
		{
			name:   "mergeability still computing is transient",
			health: &models.PullRequestHealthResponse{MergeState: models.PullRequestMergeStateMergeabilityPending},
			want:   true,
		},
		{
			name:   "conflicts are terminal",
			health: &models.PullRequestHealthResponse{MergeState: models.PullRequestMergeStateConflicted, HasConflicts: true},
			want:   false,
		},
		{
			name:   "failed checks are terminal",
			health: &models.PullRequestHealthResponse{MergeState: models.PullRequestMergeStateClean, Checks: []models.PullRequestCheckSummary{{Name: "test", Status: models.PullRequestCheckStatusFailed}}},
			want:   false,
		},
		{
			name:   "failing test count is terminal",
			health: &models.PullRequestHealthResponse{MergeState: models.PullRequestMergeStateClean, FailingTestCount: 1},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			derivePullRequestRepairActions(tt.health)
			require.Equal(t, tt.want, mergeBlockIsTransient(tt.health))
		})
	}
}

func ptrString(v string) *string {
	return &v
}

func timePtr(v time.Time) *time.Time {
	return &v
}
