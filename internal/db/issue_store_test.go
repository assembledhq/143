package db

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var issueColumns = []string{
	"id", "org_id", "external_id", "source", "source_integration_id", "repository_id",
	"title", "description", "raw_data", "status", "first_seen_at", "last_seen_at",
	"occurrence_count", "affected_customer_count", "severity", "tags", "fingerprint",
	"created_at", "updated_at",
}

func TestIssueStore_ListByOrg(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	issueID1 := uuid.New()
	issueID2 := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		filters   IssueFilters
		setupMock func(mock pgxmock.PgxPoolIface)
		expected  int
		expectErr bool
	}{
		{
			name:    "returns issues for org",
			filters: IssueFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
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
			},
			expected: 2,
		},
		{
			name:    "returns filtered issues by status",
			filters: IssueFilters{Status: "open"},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM issues WHERE org_id .+ AND status").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(issueColumns).
							AddRow(
								issueID1, orgID, "ext-1", "sentry", nil, nil,
								"Open Issue", nil, json.RawMessage(`{}`), "open", now, now,
								1, 1, "low", []string{}, "fp-3",
								now, now,
							),
					)
			},
			expected: 1,
		},
		{
			name:    "returns empty when no issues exist",
			filters: IssueFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM issues WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(issueColumns))
			},
			expected: 0,
		},
		{
			name:    "returns error on database failure",
			filters: IssueFilters{},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM issues WHERE org_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("connection refused"))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewIssueStore(mock)
			tt.setupMock(mock)

			issues, err := store.ListByOrg(context.Background(), orgID, tt.filters)
			if tt.expectErr {
				require.Error(t, err, "ListByOrg should return an error")
				return
			}
			require.NoError(t, err, "ListByOrg should not return an error")
			require.Len(t, issues, tt.expected, "should return expected number of issues")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestIssueStore_GetByID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID, issueID uuid.UUID, now time.Time)
		expectErr bool
	}{
		{
			name: "returns issue when found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, issueID uuid.UUID, now time.Time) {
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
			},
		},
		{
			name: "returns error when issue not found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, issueID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM issues WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(issueColumns))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewIssueStore(mock)
			orgID := uuid.New()
			issueID := uuid.New()
			now := time.Now()
			tt.setupMock(mock, orgID, issueID, now)

			issue, err := store.GetByID(context.Background(), orgID, issueID)
			if tt.expectErr {
				require.Error(t, err, "GetByID should return an error when issue is not found")
				return
			}
			require.NoError(t, err, "GetByID should not return an error")
			require.Equal(t, issueID, issue.ID, "should return the correct issue ID")
			require.Equal(t, "Found Issue", issue.Title, "should return the correct issue title")
			require.Equal(t, "medium", issue.Severity, "should return the correct issue severity")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestIssueStore_Upsert(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
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
	require.NoError(t, err, "Upsert should not return an error")
	require.Equal(t, generatedID, issue.ID, "should set the generated ID on the issue")
	require.Equal(t, now, issue.CreatedAt, "should set the created_at timestamp on the issue")
	require.Equal(t, now, issue.UpdatedAt, "should set the updated_at timestamp on the issue")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIssueStore_UpdateStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewIssueStore(mock)
	orgID := uuid.New()
	issueID := uuid.New()

	mock.ExpectExec("UPDATE issues SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateStatus(context.Background(), orgID, issueID, "resolved")
	require.NoError(t, err, "UpdateStatus should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestIssueStore_CountByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
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
	require.NoError(t, err, "CountByOrg should not return an error")
	require.Equal(t, 42, count, "should return the correct issue count for the org")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}
