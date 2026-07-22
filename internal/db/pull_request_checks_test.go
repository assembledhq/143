package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

var pullRequestCheckStateColumns = []string{
	"org_id", "pull_request_id", "head_sha", "source", "external_key",
	"name", "category", "status", "provider", "details_url", "summary",
	"provider_event_id", "provider_sequence", "provider_updated_at", "created_at", "updated_at",
}

func TestPullRequestStore_UpsertCheckState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		databaseApplied bool
		expectedApplied bool
	}{
		{name: "applies a newer provider event", databaseApplied: true, expectedApplied: true},
		{name: "ignores an out of order provider event", databaseApplied: false, expectedApplied: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgx mock pool")
			defer mock.Close()

			store := NewPullRequestStore(mock)
			orgID := uuid.New()
			prID := uuid.New()
			providerUpdatedAt := time.Date(2026, 7, 22, 20, 0, 0, 0, time.UTC)
			state := models.PullRequestCheckState{
				OrgID:             orgID,
				PullRequestID:     prID,
				HeadSHA:           "head-1",
				Source:            models.PullRequestCheckSourceCommitStatus,
				ExternalKey:       "ci/circleci: backend",
				Name:              "ci/circleci: backend",
				Category:          models.PullRequestCheckCategoryTest,
				Status:            models.PullRequestCheckStatusFailed,
				Provider:          "circleci",
				DetailsURL:        "https://circleci.example/build/42",
				Summary:           "Backend tests failed",
				ProviderEventID:   "delivery-42",
				ProviderSequence:  42,
				ProviderUpdatedAt: providerUpdatedAt,
			}

			mock.ExpectQuery("WITH applied AS[\\s\\S]+provider_sequence < EXCLUDED.provider_sequence").
				WithArgs(pullRequestCheckStateArgs(orgID, state)).
				WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(tt.databaseApplied))

			applied, err := store.UpsertCheckState(context.Background(), orgID, state)
			require.NoError(t, err, "UpsertCheckState should persist a valid provider event")
			require.Equal(t, tt.expectedApplied, applied, "UpsertCheckState should report whether the event changed the projection")
			require.NoError(t, mock.ExpectationsWereMet(), "all check state upsert expectations should be met")
		})
	}
}

func TestPullRequestStore_ListCheckStates(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	prID := uuid.New()
	now := time.Date(2026, 7, 22, 20, 0, 0, 0, time.UTC)
	expected := []models.PullRequestCheckState{
		{
			OrgID: orgID, PullRequestID: prID, HeadSHA: "head-1",
			Source: models.PullRequestCheckSourceCheckRun, ExternalKey: "backend test", Name: "Backend Test",
			Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPassed,
			Provider: "github-actions", DetailsURL: "https://github.example/check/7", Summary: "Passed",
			ProviderEventID: "delivery-7", ProviderSequence: 7, ProviderUpdatedAt: now, CreatedAt: now, UpdatedAt: now,
		},
	}

	mock.ExpectQuery("SELECT .+ FROM pull_request_check_states").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID, "head_sha": "head-1"}).
		WillReturnRows(pgxmock.NewRows(pullRequestCheckStateColumns).AddRow(
			orgID, prID, "head-1", models.PullRequestCheckSourceCheckRun, "backend test", "Backend Test",
			models.PullRequestCheckCategoryTest, models.PullRequestCheckStatusPassed, "github-actions",
			"https://github.example/check/7", "Passed", "delivery-7", int64(7), now, now, now,
		))

	states, err := store.ListCheckStates(context.Background(), orgID, prID, "head-1")
	require.NoError(t, err, "ListCheckStates should return the current head projection")
	require.Equal(t, expected, states, "ListCheckStates should decode every projected check field")
	require.NoError(t, mock.ExpectationsWereMet(), "all check state list expectations should be met")
}

func TestPullRequestStore_ReconcileCheckStates(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgx mock pool")
	defer mock.Close()

	store := NewPullRequestStore(mock)
	orgID := uuid.New()
	prID := uuid.New()
	state := models.PullRequestCheckState{
		OrgID: orgID, PullRequestID: prID, HeadSHA: "head-2",
		Source: models.PullRequestCheckSourceCheckRun, ExternalKey: "frontend test", Name: "Frontend Test",
		Category: models.PullRequestCheckCategoryTest, Status: models.PullRequestCheckStatusPending,
		Provider: "github-actions", ProviderSequence: 99,
		ProviderUpdatedAt: time.Date(2026, 7, 22, 21, 0, 0, 0, time.UTC),
	}

	mock.ExpectBegin()
	mock.ExpectExec("DELETE FROM pull_request_check_states").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "pull_request_id": prID, "head_sha": "head-2"}).
		WillReturnResult(pgxmock.NewResult("DELETE", 2))
	mock.ExpectExec("INSERT INTO pull_request_check_states").
		WithArgs(pullRequestCheckStateArgs(orgID, state)).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectCommit()

	err = store.ReconcileCheckStates(context.Background(), orgID, prID, "head-2", []models.PullRequestCheckState{state})
	require.NoError(t, err, "ReconcileCheckStates should atomically refresh the reconciliation baseline")
	require.NoError(t, mock.ExpectationsWereMet(), "all check state replacement expectations should be met")
}
