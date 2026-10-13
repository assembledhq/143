package db

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type sessionHistoryFixture struct {
	orgID, repoID, currentID, deletedID uuid.UUID
	newest, older                       SessionHistorySummary
}

func newSessionHistoryFixture(t *testing.T, pool *pgxpool.Pool) sessionHistoryFixture {
	t.Helper()
	ctx := t.Context()
	f := sessionHistoryFixture{orgID: uuid.New(), repoID: uuid.New(), currentID: uuid.New(), deletedID: uuid.New()}
	integrationID, otherRepoID := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO organizations(id, name) VALUES($1, $2)`, f.orgID, "history-"+f.orgID.String())
	require.NoError(t, err, "history fixture should own an isolated tenant")
	_, err = pool.Exec(ctx, `INSERT INTO integrations(id, org_id, provider) VALUES($1, $2, 'github')`, integrationID, f.orgID)
	require.NoError(t, err, "history repositories should have an owned integration")
	_, err = pool.Exec(ctx, `INSERT INTO repositories(id, org_id, integration_id, github_id, full_name, clone_url, installation_id)
		VALUES($1, $3, $4, $5, 'test/history', 'https://example.test/history.git', 1),
		      ($2, $3, $4, $6, 'test/other', 'https://example.test/other.git', 1)`, f.repoID, otherRepoID, f.orgID, integrationID,
		int64(binary.BigEndian.Uint64(f.repoID[:8])>>1), int64(binary.BigEndian.Uint64(otherRepoID[:8])>>1))
	require.NoError(t, err, "history fixture should have distinct repository scopes")
	created := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	title, result, failure := "Weekly reviewer audit", "Complete evidence ledger", "test-category"
	f.newest = SessionHistorySummary{ID: uuid.New(), Title: &title, Status: models.SessionStatusCompleted,
		Origin: models.SessionOriginAutomation, ResultSummary: &result, FailureCategory: &failure,
		CreatedAt: created.Add(time.Hour), UpdatedAt: created.Add(2 * time.Hour)}
	f.older = SessionHistorySummary{ID: uuid.New(), Title: &title, Status: models.SessionStatusFailed,
		Origin: models.SessionOriginAutomation, CreatedAt: created, UpdatedAt: created.Add(3 * time.Hour)}
	rows := []struct {
		item      SessionHistorySummary
		repoID    uuid.UUID
		deletedAt *time.Time
	}{
		{item: f.newest, repoID: f.repoID},
		{item: f.older, repoID: f.repoID},
		{item: SessionHistorySummary{ID: f.currentID, Title: &title, Status: models.SessionStatusCompleted, Origin: models.SessionOriginAutomation, CreatedAt: created.Add(4 * time.Hour), UpdatedAt: created.Add(4 * time.Hour)}, repoID: f.repoID},
		{item: SessionHistorySummary{ID: f.deletedID, Title: &title, Status: models.SessionStatusCompleted, Origin: models.SessionOriginAutomation, CreatedAt: created.Add(5 * time.Hour), UpdatedAt: created.Add(5 * time.Hour)}, repoID: f.repoID, deletedAt: &created},
		{item: SessionHistorySummary{ID: uuid.New(), Title: &title, Status: models.SessionStatusCompleted, Origin: models.SessionOriginAutomation, CreatedAt: created.Add(6 * time.Hour), UpdatedAt: created.Add(6 * time.Hour)}, repoID: otherRepoID},
	}
	for _, row := range rows {
		_, err := pool.Exec(ctx, `INSERT INTO sessions(id, org_id, repository_id, title, status, origin, result_summary, failure_category, created_at, last_activity_at, deleted_at)
			VALUES($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, row.item.ID, f.orgID, row.repoID,
			row.item.Title, row.item.Status, row.item.Origin, row.item.ResultSummary, row.item.FailureCategory,
			row.item.CreatedAt, row.item.UpdatedAt, row.deletedAt)
		require.NoError(t, err, "history row should use the fully migrated session schema")
	}
	return f
}

func normalizeSessionHistoryTimes(item SessionHistorySummary) SessionHistorySummary {
	item.CreatedAt = item.CreatedAt.UTC()
	item.UpdatedAt = item.UpdatedAt.UTC()
	return item
}

func TestSessionHistoryStoreSearchPostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name     string
		filters  func(sessionHistoryFixture) SessionHistoryFilters
		expected func(sessionHistoryFixture) []SessionHistorySummary
		foreign  bool
	}{
		{
			name: "returns complete scoped summaries ordered by creation",
			filters: func(sessionHistoryFixture) SessionHistoryFilters {
				return SessionHistoryFilters{Query: "reviewer", Limit: 10}
			},
			expected: func(f sessionHistoryFixture) []SessionHistorySummary {
				return []SessionHistorySummary{f.newest, f.older}
			},
		},
		{
			name: "cursor follows creation rather than activity time",
			filters: func(f sessionHistoryFixture) SessionHistoryFilters {
				return SessionHistoryFilters{Limit: 1, Cursor: f.newest.ID.String()}
			},
			expected: func(f sessionHistoryFixture) []SessionHistorySummary { return []SessionHistorySummary{f.older} },
		},
		{
			name: "status and date filters retain activity timestamp",
			filters: func(f sessionHistoryFixture) SessionHistoryFilters {
				return SessionHistoryFilters{Status: string(models.SessionStatusCompleted), CreatedAfter: &f.newest.CreatedAt, CreatedBefore: &f.newest.CreatedAt}
			},
			expected: func(f sessionHistoryFixture) []SessionHistorySummary { return []SessionHistorySummary{f.newest} },
		},
		{
			name:     "another tenant cannot search a known repository",
			filters:  func(sessionHistoryFixture) SessionHistoryFilters { return SessionHistoryFilters{} },
			expected: func(sessionHistoryFixture) []SessionHistorySummary { return []SessionHistorySummary{} },
			foreign:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSessionHistoryFixture(t, pool)
			orgID := f.orgID
			if tt.foreign {
				orgID = uuid.New()
			}
			actual, err := NewSessionHistoryStore(pool).Search(t.Context(), orgID, f.repoID, f.currentID, tt.filters(f))
			require.NoError(t, err, "history search should execute against sessions without an updated_at column")
			for i, item := range actual {
				actual[i] = normalizeSessionHistoryTimes(item)
			}
			require.Equal(t, tt.expected(f), actual, "history should preserve complete summaries and enforce tenant, repository, deletion, current-session, and pagination filters")
		})
	}
}

func TestSessionHistoryStoreGetPostgres(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name                        string
		foreign, otherRepo, deleted bool
	}{
		{name: "returns activity timestamp under the existing response field"},
		{name: "another tenant cannot read a known session", foreign: true},
		{name: "another repository cannot read a known session", otherRepo: true},
		{name: "deleted session is excluded", deleted: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSessionHistoryFixture(t, pool)
			orgID, repoID, sessionID := f.orgID, f.repoID, f.older.ID
			if tt.foreign {
				orgID = uuid.New()
			}
			if tt.otherRepo {
				repoID = uuid.New()
			}
			if tt.deleted {
				sessionID = f.deletedID
			}
			actual, err := NewSessionHistoryStore(pool).Get(t.Context(), orgID, repoID, sessionID)
			if tt.foreign || tt.otherRepo || tt.deleted {
				require.ErrorIs(t, err, pgx.ErrNoRows, "history detail should hide sessions outside its tenant, repository, or live-row scope")
				require.Equal(t, SessionHistorySummary{}, actual, "denied history reads should not disclose any summary fields")
				return
			}
			require.NoError(t, err, "history detail should execute against sessions without an updated_at column")
			require.Equal(t, f.older, normalizeSessionHistoryTimes(actual), "history detail should return every field and the session's last activity time")
		})
	}
}
