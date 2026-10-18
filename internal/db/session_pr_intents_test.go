package db

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func newSessionPRIntentFixture() models.SessionPRIntent {
	title := "docs: Weekly dependency report"
	body := "## Summary\n\nSource plan: docs/plans/weekly.md"
	return models.SessionPRIntent{OrgID: uuid.New(), SessionID: uuid.New(), AutomationRunID: uuid.New(), ChangesetID: uuid.New(), TeamReviewers: []string{"example/reviewers"}, Title: &title, Body: &body}
}

func TestSessionPRIntentStage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		setup    func(pgxmock.PgxPoolIface, models.SessionPRIntent)
		expected error
	}{
		{"scoped metadata persisted", func(mock pgxmock.PgxPoolIface, intent models.SessionPRIntent) {
			mock.ExpectQuery(`(?s)INSERT INTO session_pr_intents.*ar.org_id = sal.org_id.*repo.org_id = sess.org_id.*sc.is_primary.*sal.org_id = \$1 AND sal.session_id = \$2 AND sal.automation_run_id = \$3 AND sc.id = \$4.*p.org_id = \$1.*frozen_at IS NULL`).
				WithArgs(intent.OrgID, intent.SessionID, intent.AutomationRunID, intent.ChangesetID, intent.TeamReviewers, intent.Title, intent.Body).
				WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow(intent.SessionID))
		}, nil},
		{"frozen snapshot rejected", func(mock pgxmock.PgxPoolIface, intent models.SessionPRIntent) {
			mock.ExpectQuery(`INSERT INTO session_pr_intents`).WithArgs(intent.OrgID, intent.SessionID, intent.AutomationRunID, intent.ChangesetID, intent.TeamReviewers, intent.Title, intent.Body).
				WillReturnRows(pgxmock.NewRows([]string{"session_id"}))
			mock.ExpectQuery(`(?s)SELECT EXISTS.*org_id = \$1 AND session_id = \$2 AND automation_run_id = \$3 AND changeset_id = \$4`).
				WithArgs(intent.OrgID, intent.SessionID, intent.AutomationRunID, intent.ChangesetID).WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))
		}, ErrSessionPRIntentFrozen},
		{"missing scope rejected", func(mock pgxmock.PgxPoolIface, intent models.SessionPRIntent) {
			mock.ExpectQuery(`INSERT INTO session_pr_intents`).WithArgs(intent.OrgID, intent.SessionID, intent.AutomationRunID, intent.ChangesetID, intent.TeamReviewers, intent.Title, intent.Body).
				WillReturnRows(pgxmock.NewRows([]string{"session_id"}))
			mock.ExpectQuery(`SELECT EXISTS`).WithArgs(intent.OrgID, intent.SessionID, intent.AutomationRunID, intent.ChangesetID).
				WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))
		}, pgx.ErrNoRows},
		{"staged text merges with earlier fields", func(mock pgxmock.PgxPoolIface, intent models.SessionPRIntent) {
			mock.ExpectQuery(`(?s)INSERT INTO session_pr_intents.*team_reviewers, title, body.*COALESCE\(\$5::text\[\], '\{\}'\), \$6::text, \$7::text.*team_reviewers = COALESCE\(\$5::text\[\], session_pr_intents.team_reviewers\).*title = COALESCE\(\$6::text, session_pr_intents.title\).*body = COALESCE\(\$7::text, session_pr_intents.body\).*frozen_at IS NULL`).
				WithArgs(intent.OrgID, intent.SessionID, intent.AutomationRunID, intent.ChangesetID, intent.TeamReviewers, intent.Title, intent.Body).
				WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow(intent.SessionID))
		}, nil},
		{"write error propagated", func(mock pgxmock.PgxPoolIface, intent models.SessionPRIntent) {
			mock.ExpectQuery(`INSERT INTO session_pr_intents`).WithArgs(intent.OrgID, intent.SessionID, intent.AutomationRunID, intent.ChangesetID, intent.TeamReviewers, intent.Title, intent.Body).WillReturnError(context.Canceled)
		}, context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "database mock should initialize")
			t.Cleanup(mock.Close)
			intent := newSessionPRIntentFixture()
			tt.setup(mock, intent)
			err = NewSessionPRIntentStore(mock).Stage(context.Background(), intent.OrgID, intent)
			require.ErrorIs(t, err, tt.expected, "staging should preserve scoped storage and frozen-state errors")
			require.NoError(t, mock.ExpectationsWereMet(), "all scoped database expectations should be met")
		})
	}
}

func TestSessionPRIntentStageValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*models.SessionPRIntent)
	}{
		{"wrong organization", func(i *models.SessionPRIntent) { i.OrgID = uuid.New() }},
		{"missing session", func(i *models.SessionPRIntent) { i.SessionID = uuid.Nil }},
		{"missing run", func(i *models.SessionPRIntent) { i.AutomationRunID = uuid.Nil }},
		{"missing changeset", func(i *models.SessionPRIntent) { i.ChangesetID = uuid.Nil }},
		{"bare slug", func(i *models.SessionPRIntent) { i.TeamReviewers = []string{"reviewers"} }},
		{"mixed case", func(i *models.SessionPRIntent) { i.TeamReviewers = []string{"example/Reviewers"} }},
		{"duplicate", func(i *models.SessionPRIntent) { i.TeamReviewers = []string{"example/reviewers", "example/reviewers"} }},
		{"too many", func(i *models.SessionPRIntent) {
			i.TeamReviewers = strings.Split(strings.Repeat("example/reviewers,", 10)+"example/last", ",")
		}},
		{"untrimmed title", func(i *models.SessionPRIntent) {
			title := " docs: Weekly report"
			i.Title = &title
		}},
		{"multiline title", func(i *models.SessionPRIntent) {
			title := "docs: Weekly\nreport"
			i.Title = &title
		}},
		{"blank body", func(i *models.SessionPRIntent) {
			body := "\n\t"
			i.Body = &body
		}},
		{"oversized body", func(i *models.SessionPRIntent) {
			body := strings.Repeat("a", models.MaxStagedPRBodyLength+1)
			i.Body = &body
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "database mock should initialize")
			t.Cleanup(mock.Close)
			intent := newSessionPRIntentFixture()
			orgID := intent.OrgID
			tt.change(&intent)
			require.Error(t, NewSessionPRIntentStore(mock).Stage(context.Background(), orgID, intent), "invalid scope and noncanonical metadata must fail before SQL")
			require.NoError(t, mock.ExpectationsWereMet(), "invalid metadata must not touch the database")
		})
	}
}

func TestSessionPRIntentFreeze(t *testing.T) {
	t.Parallel()
	intent := newSessionPRIntentFixture()
	now := time.Now().UTC()
	intent.FrozenAt, intent.CreatedAt, intent.UpdatedAt = &now, now, now
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "database mock should initialize")
	t.Cleanup(mock.Close)
	mock.ExpectQuery(`(?s)INSERT INTO session_pr_intents.*ar.org_id = sal.org_id.*sc.is_primary.*sal.org_id = \$1.*COALESCE\(session_pr_intents.frozen_at, EXCLUDED.frozen_at\)`).
		WithArgs(intent.OrgID, intent.SessionID, intent.AutomationRunID, intent.ChangesetID).
		WillReturnRows(pgxmock.NewRows(strings.Fields(strings.ReplaceAll(sessionPRIntentColumns, ",", ""))).
			AddRow(intent.OrgID, intent.SessionID, intent.AutomationRunID, intent.ChangesetID, intent.TeamReviewers, intent.Title, intent.Body, intent.FrozenAt, now, now))
	actual, err := NewSessionPRIntentStore(mock).Freeze(context.Background(), intent.OrgID, intent.SessionID, intent.AutomationRunID, intent.ChangesetID)
	require.NoError(t, err, "freezing scoped metadata should succeed")
	require.Equal(t, intent, actual, "freezing should return the durable immutable snapshot")
	require.NoError(t, mock.ExpectationsWereMet(), "freeze should apply all scope filters")
}

func teamReviewReceiptRows(receipt models.TeamReviewRequestReceipt) *pgxmock.Rows {
	return pgxmock.NewRows([]string{"org_id", "publication_id", "team", "state", "created_at", "updated_at"}).
		AddRow(receipt.OrgID, receipt.PublicationID, receipt.Team, receipt.State, receipt.CreatedAt, receipt.UpdatedAt)
}

func TestTeamReviewRequestBegin(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		state   models.TeamReviewRequestState
		claimed bool
	}{
		{"fresh reservation claims once", models.TeamReviewRequestSending, true},
		{"uncertain send is not reclaimed", models.TeamReviewRequestSending, false},
		{"confirmed send is not reclaimed", models.TeamReviewRequestRequested, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "database mock should initialize")
			t.Cleanup(mock.Close)
			now := time.Now().UTC()
			receipt := models.TeamReviewRequestReceipt{OrgID: uuid.New(), PublicationID: uuid.New(), Team: "example/reviewers", State: tt.state, CreatedAt: now, UpdatedAt: now}
			insert := mock.ExpectQuery(`(?s)INSERT INTO publication_team_review_requests.*p.org_id = \$1 AND p.id = \$2.*state = 'retryable'`).WithArgs(receipt.OrgID, receipt.PublicationID, receipt.Team)
			if tt.claimed {
				insert.WillReturnRows(teamReviewReceiptRows(receipt))
			} else {
				insert.WillReturnRows(pgxmock.NewRows([]string{"org_id", "publication_id", "team", "state", "created_at", "updated_at"}))
				mock.ExpectQuery(`(?s)SELECT.*FROM publication_team_review_requests.*org_id = \$1 AND publication_id = \$2 AND team = \$3`).WithArgs(receipt.OrgID, receipt.PublicationID, receipt.Team).WillReturnRows(teamReviewReceiptRows(receipt))
			}
			actual, claimed, err := NewTeamReviewRequestStore(mock).Begin(context.Background(), receipt.OrgID, receipt.PublicationID, receipt.Team)
			require.NoError(t, err, "reservation should return its durable receipt")
			require.Equal(t, receipt, actual, "receipt identity and state should survive replay")
			require.Equal(t, tt.claimed, claimed, "only a new or explicitly retryable reservation may send")
			require.NoError(t, mock.ExpectationsWereMet(), "reservation and replay must scope organization, publication and team")
		})
	}
}

func TestTeamReviewRequestStateWrites(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		retryable bool
		rows      int64
		writeErr  error
		expected  error
	}{
		{name: "confirmed reservation", rows: 1},
		{name: "definitive rejection", retryable: true, rows: 1},
		{name: "missing confirmation reservation", expected: pgx.ErrNoRows},
		{name: "confirmed receipt cannot be reset", retryable: true, expected: pgx.ErrNoRows},
		{name: "confirmation failure propagated", writeErr: context.Canceled, expected: context.Canceled},
		{name: "rejection failure propagated", retryable: true, writeErr: context.Canceled, expected: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "database mock should initialize")
			t.Cleanup(mock.Close)
			orgID, publicationID := uuid.New(), uuid.New()
			query := `UPDATE publication_team_review_requests\s+SET state = 'requested', updated_at = now\(\)\s+WHERE org_id = \$1 AND publication_id = \$2 AND team = \$3 AND state IN \('sending', 'requested'\)`
			if tt.retryable {
				query = `UPDATE publication_team_review_requests\s+SET state = 'retryable', updated_at = now\(\)\s+WHERE org_id = \$1 AND publication_id = \$2 AND team = \$3 AND state = 'sending'`
			}
			expectation := mock.ExpectExec(query).WithArgs(orgID, publicationID, "example/reviewers")
			if tt.writeErr != nil {
				expectation.WillReturnError(tt.writeErr)
			} else {
				expectation.WillReturnResult(pgxmock.NewResult("UPDATE", tt.rows))
			}
			store := NewTeamReviewRequestStore(mock)
			if tt.retryable {
				err = store.MarkRetryable(context.Background(), orgID, publicationID, "example/reviewers")
			} else {
				err = store.MarkRequested(context.Background(), orgID, publicationID, "example/reviewers")
			}
			require.ErrorIs(t, err, tt.expected, "state writes must preserve storage errors and avoid resetting confirmed receipts")
			require.NoError(t, mock.ExpectationsWereMet(), "state writes must scope organization, publication and team")
		})
	}
}

func TestTeamReviewRequestBeginErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		setup    func(pgxmock.PgxPoolIface, uuid.UUID, uuid.UUID)
		expected error
	}{
		{"insert failure", func(mock pgxmock.PgxPoolIface, orgID, publicationID uuid.UUID) {
			mock.ExpectQuery(`INSERT INTO publication_team_review_requests`).WithArgs(orgID, publicationID, "example/reviewers").WillReturnError(context.Canceled)
		}, context.Canceled},
		{"missing publication", func(mock pgxmock.PgxPoolIface, orgID, publicationID uuid.UUID) {
			mock.ExpectQuery(`INSERT INTO publication_team_review_requests`).WithArgs(orgID, publicationID, "example/reviewers").WillReturnRows(pgxmock.NewRows([]string{"org_id"}))
			mock.ExpectQuery(regexp.QuoteMeta("SELECT "+teamReviewRequestColumns)).WithArgs(orgID, publicationID, "example/reviewers").WillReturnRows(pgxmock.NewRows([]string{"org_id"}))
		}, pgx.ErrNoRows},
		{"replay read failure", func(mock pgxmock.PgxPoolIface, orgID, publicationID uuid.UUID) {
			mock.ExpectQuery(`INSERT INTO publication_team_review_requests`).WithArgs(orgID, publicationID, "example/reviewers").WillReturnRows(pgxmock.NewRows([]string{"org_id"}))
			mock.ExpectQuery(`SELECT`).WithArgs(orgID, publicationID, "example/reviewers").WillReturnError(context.Canceled)
		}, context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "database mock should initialize")
			t.Cleanup(mock.Close)
			orgID, publicationID := uuid.New(), uuid.New()
			tt.setup(mock, orgID, publicationID)
			_, claimed, err := NewTeamReviewRequestStore(mock).Begin(context.Background(), orgID, publicationID, "example/reviewers")
			require.True(t, errors.Is(err, tt.expected), "reservation failures must propagate their cause")
			require.False(t, claimed, "storage failure must never authorize an external send")
			require.NoError(t, mock.ExpectationsWereMet(), "all failure-path queries should be exercised")
		})
	}
}
