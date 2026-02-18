package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var issueColumns = []string{
	"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
	"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
	"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
	"created_at", "updated_at",
}

func TestIssueStore_ListByOrg_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewIssueStore(mock)
	orgID := uuid.New()
	issueID1 := uuid.New()
	issueID2 := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM issues WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(issueColumns).
				AddRow(
					issueID1, orgID, "ext-1", "sentry", nil, nil,
					"Issue One", nil, json.RawMessage(`{}`), "open", now, now,
					5, 2, "high", []string{"bug"}, "fp-1",
					now, now,
				).
				AddRow(
					issueID2, orgID, "ext-2", "github", nil, nil,
					"Issue Two", nil, json.RawMessage(`{}`), "open", now, now,
					3, 1, "medium", []string{"perf"}, "fp-2",
					now, now,
				),
		)

	issues, err := store.ListByOrg(context.Background(), orgID, IssueFilters{})
	require.NoError(t, err)
	assert.Len(t, issues, 2)
	assert.Equal(t, issueID1, issues[0].ID)
	assert.Equal(t, "Issue One", issues[0].Title)
	assert.Equal(t, issueID2, issues[1].ID)
	assert.Equal(t, "Issue Two", issues[1].Title)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIssueStore_ListByOrg_WithFilters(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewIssueStore(mock)
	orgID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM issues WHERE org_id .+ AND status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(issueColumns).
				AddRow(
					issueID, orgID, "ext-1", "sentry", nil, nil,
					"Open Issue", nil, json.RawMessage(`{}`), "open", now, now,
					1, 1, "low", []string{}, "fp-3",
					now, now,
				),
		)

	issues, err := store.ListByOrg(context.Background(), orgID, IssueFilters{Status: "open"})
	require.NoError(t, err)
	assert.Len(t, issues, 1)
	assert.Equal(t, "open", issues[0].Status)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIssueStore_ListByOrg_Empty(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewIssueStore(mock)
	orgID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM issues WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(issueColumns))

	issues, err := store.ListByOrg(context.Background(), orgID, IssueFilters{})
	require.NoError(t, err)
	assert.Empty(t, issues)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIssueStore_GetByID_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewIssueStore(mock)
	orgID := uuid.New()
	issueID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM issues WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(issueColumns).
				AddRow(
					issueID, orgID, "ext-1", "sentry", nil, nil,
					"Found Issue", nil, json.RawMessage(`{}`), "open", now, now,
					3, 1, "medium", []string{"bug"}, "fp-found",
					now, now,
				),
		)

	issue, err := store.GetByID(context.Background(), orgID, issueID)
	require.NoError(t, err)
	assert.Equal(t, issueID, issue.ID)
	assert.Equal(t, "Found Issue", issue.Title)
	assert.Equal(t, "medium", issue.Severity)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIssueStore_GetByID_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewIssueStore(mock)
	orgID := uuid.New()
	issueID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM issues WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(issueColumns))

	_, err = store.GetByID(context.Background(), orgID, issueID)
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIssueStore_Upsert_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewIssueStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	issue := &models.Issue{
		OrgID:                 uuid.New(),
		ExternalID:            "ext-upsert",
		Source:                "sentry",
		Title:                 "Upsert Issue",
		Status:                "open",
		RawData:               json.RawMessage(`{"key":"value"}`),
		FirstSeenAt:           now,
		LastSeenAt:            now,
		OccurrenceCount:       1,
		AffectedCustomerCount: 1,
		Severity:              "high",
		Tags:                  []string{"new"},
		Fingerprint:           "fp-upsert",
	}

	mock.ExpectQuery("INSERT INTO issues").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).
				AddRow(generatedID, now, now),
		)

	err = store.Upsert(context.Background(), issue)
	require.NoError(t, err)
	assert.Equal(t, generatedID, issue.ID)
	assert.Equal(t, now, issue.CreatedAt)
	assert.Equal(t, now, issue.UpdatedAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIssueStore_UpdateStatus_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewIssueStore(mock)
	orgID := uuid.New()
	issueID := uuid.New()

	mock.ExpectExec("UPDATE issues SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateStatus(context.Background(), orgID, issueID, "resolved")
	require.NoError(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestIssueStore_CountByOrg_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewIssueStore(mock)
	orgID := uuid.New()

	mock.ExpectQuery("SELECT count").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"count"}).
				AddRow(42),
		)

	count, err := store.CountByOrg(context.Background(), orgID)
	require.NoError(t, err)
	assert.Equal(t, 42, count)
	assert.NoError(t, mock.ExpectationsWereMet())
}
