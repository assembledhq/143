package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var sessionIssueLinkTestColumns = []string{
	"id", "org_id", "session_id", "issue_id", "role",
	"position", "added_by_user_id", "created_at",
	"issue_title", "issue_source", "external_id", "description",
	"repository_id", "issue_status",
	// Migration 102 — Linear workspace slug is left-joined off
	// session_issue_link_provider_state for deep-link rendering.
	"issue_workspace_slug",
	"linear_last_skipped_reason",
	"linear_primary_snapshot",
}

func TestSessionIssueLinkStore_Create(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		rowsAffected int64
		execErr      error
		expectErrIs  error
	}{
		{
			name:         "creates valid link",
			rowsAffected: 1,
		},
		{
			name:         "returns invalid link when insert affects no rows",
			rowsAffected: 0,
			expectErrIs:  ErrInvalidSessionIssueLink,
		},
		{
			name:    "wraps database errors",
			execErr: errors.New("db unavailable"),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewSessionIssueLinkStore(mock)
			orgID := uuid.New()
			sessionID := uuid.New()
			issueID := uuid.New()
			userID := uuid.New()

			expectation := mock.ExpectExec("INSERT INTO session_issue_links").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg())
			if tt.execErr != nil {
				expectation.WillReturnError(tt.execErr)
			} else {
				expectation.WillReturnResult(pgxmock.NewResult("INSERT", tt.rowsAffected))
			}

			err = store.Create(context.Background(), orgID, sessionID, issueID, models.SessionIssueLinkRolePrimary, 0, &userID)
			if tt.expectErrIs != nil {
				require.ErrorIs(t, err, tt.expectErrIs, "Create should surface the expected sentinel error")
			} else if tt.execErr != nil {
				require.Error(t, err, "Create should return database errors")
				require.Contains(t, err.Error(), "insert session issue link", "Create should wrap database errors with context")
			} else {
				require.NoError(t, err, "Create should succeed for valid links")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSessionIssueLinkStore_CreateAllowingNullRepo_AllowsRepoLessSessions(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionIssueLinkStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	linkID := uuid.New()

	mock.ExpectQuery(`INSERT INTO session_issue_links[\s\S]+AND \(s\.repository_id IS NULL OR i\.repository_id IS NULL OR s\.repository_id = i\.repository_id\)`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(linkID))

	got, err := store.CreateAllowingNullRepo(context.Background(), orgID, sessionID, issueID, models.SessionIssueLinkRolePrimary, 0, nil)
	require.NoError(t, err, "CreateAllowingNullRepo should allow issue-only sessions with no repository")
	require.Equal(t, linkID, got, "CreateAllowingNullRepo should return the inserted link id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionIssueLinkStore_CreateAllowingNullRepo_ReturnsExistingOnConflict(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionIssueLinkStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	existingID := uuid.New()

	mock.ExpectQuery("INSERT INTO session_issue_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery("SELECT id FROM session_issue_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(existingID))

	got, err := store.CreateAllowingNullRepo(context.Background(), orgID, sessionID, issueID, models.SessionIssueLinkRolePrimary, 0, nil)
	require.NoError(t, err, "CreateAllowingNullRepo should treat an existing link as idempotent success")
	require.Equal(t, existingID, got, "CreateAllowingNullRepo should return the existing link id")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionIssueLinkSelectColumns_UsesLinearIdentifierForExternalID(t *testing.T) {
	t.Parallel()

	require.Contains(t, sessionIssueLinkSelectColumns, "NULLIF(provider_state.state->>'identifier', '')", "linked issue enrichment should expose the human Linear key when provider state has it")
}

func TestSessionIssueLinkSelectColumns_FallsBackToLinearTitleIdentifier(t *testing.T) {
	t.Parallel()

	require.Contains(t, sessionIssueLinkSelectColumns, "CASE WHEN i.source = 'linear' THEN substring(i.title from '^([A-Z][A-Z0-9_]{0,9}-[0-9]+):') END", "issue-triggered Linear sessions should expose the human Linear key even when provider state was never seeded")
}

func TestSessionIssueLinkSelectColumns_ExposesLinearLastSkippedReason(t *testing.T) {
	t.Parallel()

	require.Contains(t, sessionIssueLinkSelectColumns, "(provider_state.state->>'last_skipped_reason') AS linear_last_skipped_reason", "linked issue enrichment should expose the latest Linear state-sync skip reason for operator debugging")
}

func TestSessionIssueLinkStore_ListBySession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionIssueLinkStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	issueID := uuid.New()
	repoID := uuid.New()
	addedBy := uuid.New()
	now := time.Now().UTC()
	title := "Fix checkout timeout"
	source := models.IssueSourceLinear
	externalID := "ENG-123"
	description := "Customers hit a timeout after payment authorization."
	status := "open"
	lastSkippedReason := "user_recent_edit"

	mock.ExpectQuery("SELECT .+ FROM session_issue_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionIssueLinkTestColumns).AddRow(
				uuid.New(), orgID, sessionID, issueID, string(models.SessionIssueLinkRolePrimary),
				0, &addedBy, now,
				&title, &source, &externalID, &description,
				&repoID, &status,
				nil, // issue_workspace_slug
				&lastSkippedReason,
				nil, // linear_primary_snapshot
			),
		)

	links, err := store.ListBySession(context.Background(), orgID, sessionID)
	require.NoError(t, err, "ListBySession should return links when the query succeeds")
	require.Len(t, links, 1, "ListBySession should return the mocked row")
	require.Equal(t, issueID, links[0].IssueID, "ListBySession should decode the linked issue id")
	require.Equal(t, models.SessionIssueLinkRolePrimary, links[0].Role, "ListBySession should decode the link role")
	require.Equal(t, title, *links[0].IssueTitle, "ListBySession should decode issue enrichment fields")
	require.Equal(t, lastSkippedReason, *links[0].LinearLastSkippedReason, "ListBySession should decode the latest Linear skip reason for operator debugging")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionIssueLinkStore_ListBySession_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewSessionIssueLinkStore(mock)
	mock.ExpectQuery("SELECT .+ FROM session_issue_links").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("db unavailable"))

	_, err = store.ListBySession(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err, "ListBySession should return query errors")
	require.Contains(t, err.Error(), "query session issue links", "ListBySession should wrap query errors with context")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestSessionIssueLinkStore_GetByIDAndRemove(t *testing.T) {
	t.Parallel()

	t.Run("gets enriched link by id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		store := NewSessionIssueLinkStore(mock)
		orgID := uuid.New()
		sessionID := uuid.New()
		issueID := uuid.New()
		linkID := uuid.New()
		now := time.Now().UTC()
		title := "Fix session linking"
		source := models.IssueSourceLinear
		externalID := "ACS-123"
		status := "open"
		workspace := "acme"

		mock.ExpectQuery("SELECT .+ FROM session_issue_links").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(sessionIssueLinkTestColumns).AddRow(
				linkID, orgID, sessionID, issueID, string(models.SessionIssueLinkRolePrimary),
				0, nil, now,
				&title, &source, &externalID, nil,
				nil, &status, &workspace, nil, nil,
			))

		got, err := store.GetByID(context.Background(), orgID, linkID)
		require.NoError(t, err, "GetByID should return the enriched link")
		require.Equal(t, linkID, got.ID, "GetByID should decode the link id")
		require.Equal(t, workspace, *got.IssueWorkspaceSlug, "GetByID should decode the Linear workspace slug")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("removes link and returns session id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		store := NewSessionIssueLinkStore(mock)
		sessionID := uuid.New()
		mock.ExpectQuery("DELETE FROM session_issue_links").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow(sessionID))

		got, err := store.Remove(context.Background(), uuid.New(), uuid.New())
		require.NoError(t, err, "Remove should delete the link")
		require.Equal(t, sessionID, got, "Remove should return the owning session id")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("remove maps missing rows", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		store := NewSessionIssueLinkStore(mock)
		mock.ExpectQuery("DELETE FROM session_issue_links").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(pgx.ErrNoRows)

		_, err = store.Remove(context.Background(), uuid.New(), uuid.New())
		require.ErrorIs(t, err, ErrInvalidSessionIssueLink, "Remove should map missing rows to ErrInvalidSessionIssueLink")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}

func TestSessionIssueLinkStore_ListBySessionIDs(t *testing.T) {
	t.Parallel()

	t.Run("returns empty map for no session ids", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		store := NewSessionIssueLinkStore(mock)
		grouped, err := store.ListBySessionIDs(context.Background(), uuid.New(), nil)
		require.NoError(t, err, "ListBySessionIDs should treat an empty input as a no-op")
		require.Empty(t, grouped, "ListBySessionIDs should return an empty map for an empty input")
		require.NoError(t, mock.ExpectationsWereMet(), "there should be no database work for an empty input")
	})

	t.Run("groups links by session id", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		store := NewSessionIssueLinkStore(mock)
		orgID := uuid.New()
		sessionA := uuid.New()
		sessionB := uuid.New()
		repoID := uuid.New()
		now := time.Now().UTC()

		mock.ExpectQuery("SELECT .+ FROM session_issue_links").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(sessionIssueLinkTestColumns).
					AddRow(uuid.New(), orgID, sessionA, uuid.New(), string(models.SessionIssueLinkRolePrimary), 0, nil, now, nil, nil, nil, nil, &repoID, nil, nil, nil, nil).
					AddRow(uuid.New(), orgID, sessionA, uuid.New(), string(models.SessionIssueLinkRoleRelated), 1, nil, now, nil, nil, nil, nil, &repoID, nil, nil, nil, nil).
					AddRow(uuid.New(), orgID, sessionB, uuid.New(), string(models.SessionIssueLinkRolePrimary), 0, nil, now, nil, nil, nil, nil, &repoID, nil, nil, nil, nil),
			)

		grouped, err := store.ListBySessionIDs(context.Background(), orgID, []uuid.UUID{sessionA, sessionB})
		require.NoError(t, err, "ListBySessionIDs should return grouped links when the query succeeds")
		require.Len(t, grouped[sessionA], 2, "ListBySessionIDs should group multiple links under the same session")
		require.Len(t, grouped[sessionB], 1, "ListBySessionIDs should group links for the second session separately")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("returns query errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		store := NewSessionIssueLinkStore(mock)
		mock.ExpectQuery("SELECT .+ FROM session_issue_links").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("db unavailable"))

		_, err = store.ListBySessionIDs(context.Background(), uuid.New(), []uuid.UUID{uuid.New()})
		require.Error(t, err, "ListBySessionIDs should return query errors")
		require.Contains(t, err.Error(), "query session issue links by session ids", "ListBySessionIDs should wrap query errors with context")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("returns collect errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create mock pool")
		defer mock.Close()

		store := NewSessionIssueLinkStore(mock)
		mock.ExpectQuery("SELECT .+ FROM session_issue_links").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows(sessionIssueLinkTestColumns).AddRow(
					"bad-uuid", uuid.New(), uuid.New(), uuid.New(), string(models.SessionIssueLinkRolePrimary),
					0, nil, time.Now().UTC(), nil, nil, nil, nil, nil, nil, nil, nil, nil,
				),
			)

		_, err = store.ListBySessionIDs(context.Background(), uuid.New(), []uuid.UUID{uuid.New()})
		require.Error(t, err, "ListBySessionIDs should return row decoding errors")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}
