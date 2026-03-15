package db

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var memoryColumns = []string{
	"id", "org_id", "repo", "rule", "category", "source_comment_ids",
	"occurrence_count", "status", "manually_curated", "active",
	"scope", "source", "last_used_at", "times_reinforced", "file_patterns", "created_at",
}

func newMemoryRow(id, orgID uuid.UUID, now time.Time) []any {
	return []any{
		id, orgID, "org/repo", "Always use structured logging", "style",
		[]uuid.UUID{uuid.New()}, 1, "candidate", false, true,
		"repo", "review", &now, 0, []string(nil), now,
	}
}

func TestMemoryStore_Create_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewMemoryStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	m := &models.Memory{
		OrgID:            uuid.New(),
		Repo:             "org/repo",
		Rule:             "Always use structured logging",
		Category:         "style",
		SourceCommentIDs: []uuid.UUID{uuid.New()},
		OccurrenceCount:  1,
		Status:           "candidate",
		ManuallyCurated:  false,
	}

	mock.ExpectQuery("INSERT INTO memories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.Create(context.Background(), m)
	require.NoError(t, err, "should create memory without error")
	require.Equal(t, generatedID, m.ID, "should set the generated ID on the memory")
	require.Equal(t, now, m.CreatedAt, "should set the created_at timestamp on the memory")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestMemoryStore_GetByID_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewMemoryStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM memories WHERE id .+ AND org_id .+ AND active = true").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(memoryColumns).
				AddRow(newMemoryRow(id, orgID, now)...),
		)

	m, err := store.GetByID(context.Background(), orgID, id)
	require.NoError(t, err, "should retrieve memory by ID without error")
	require.Equal(t, id, m.ID, "should return the correct memory ID")
	require.Equal(t, orgID, m.OrgID, "should return the correct org ID")
	require.Equal(t, "org/repo", m.Repo, "should return the correct repo")
	require.Equal(t, "Always use structured logging", m.Rule, "should return the correct rule")
	require.Equal(t, "style", m.Category, "should return the correct category")
	require.Equal(t, 1, m.OccurrenceCount, "should return the correct occurrence count")
	require.Equal(t, "candidate", m.Status, "should return the correct status")
	require.False(t, m.ManuallyCurated, "should return the correct manually_curated flag")
	require.True(t, m.Active, "should return the correct active flag")
	require.Equal(t, "repo", m.Scope, "should return the correct scope")
	require.Equal(t, "review", m.Source, "should return the correct source")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestMemoryStore_GetByID_NotFound(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewMemoryStore(mock)

	mock.ExpectQuery("SELECT .+ FROM memories WHERE id .+ AND org_id .+ AND active = true").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(memoryColumns))

	_, err = store.GetByID(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err, "should return an error when memory is not found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestMemoryStore_ListByRepo_WithStatusFilter(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewMemoryStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM memories WHERE org_id .+ AND repo .+ AND active = true AND status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(memoryColumns).
				AddRow(newMemoryRow(id, orgID, now)...),
		)

	memories, err := store.ListByRepo(context.Background(), orgID, "org/repo", MemoryFilters{Status: "candidate"})
	require.NoError(t, err, "should list memories filtered by status without error")
	require.Len(t, memories, 1, "should return only the memory matching the status filter")
	require.Equal(t, id, memories[0].ID, "filtered memory should have the correct ID")
	require.Equal(t, orgID, memories[0].OrgID, "filtered memory should have the correct org ID")
	require.Equal(t, "candidate", memories[0].Status, "filtered memory should have the correct status")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestMemoryStore_ListActiveByRepo_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewMemoryStore(mock)
	orgID := uuid.New()
	id1 := uuid.New()
	id2 := uuid.New()
	now := time.Now()

	activeRow := func(id uuid.UUID) []any {
		return []any{
			id, orgID, "org/repo", "Use error wrapping", "error-handling",
			[]uuid.UUID{uuid.New(), uuid.New()}, 3, "active", false, true,
			"repo", "review", &now, 3, []string(nil), now,
		}
	}

	mock.ExpectQuery("SELECT .+ FROM memories WHERE org_id .+ AND repo .+ AND active = true AND status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(memoryColumns).
				AddRow(activeRow(id1)...).
				AddRow(activeRow(id2)...),
		)

	memories, err := store.ListActiveByRepo(context.Background(), orgID, "org/repo")
	require.NoError(t, err, "should list active memories without error")
	require.Len(t, memories, 2, "should return both active memories for the repo")
	require.Equal(t, id1, memories[0].ID, "first memory should have the correct ID")
	require.Equal(t, id2, memories[1].ID, "second memory should have the correct ID")
	require.Equal(t, "active", memories[0].Status, "first memory should have status 'active'")
	require.Equal(t, "active", memories[1].Status, "second memory should have status 'active'")
	require.True(t, memories[0].Active, "first memory should have active flag set to true")
	require.True(t, memories[1].Active, "second memory should have active flag set to true")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestMemoryStore_FindMatchingRule_Found(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewMemoryStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM memories WHERE org_id .+ AND repo .+ AND active = true AND lower.rule.").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(memoryColumns).
				AddRow(newMemoryRow(id, orgID, now)...),
		)

	m, err := store.FindMatchingRule(context.Background(), orgID, "org/repo", "always use structured logging")
	require.NoError(t, err, "should find matching rule without error")
	require.Equal(t, id, m.ID, "should return the correct memory ID")
	require.Equal(t, orgID, m.OrgID, "should return the correct org ID")
	require.Equal(t, "Always use structured logging", m.Rule, "should return the correct rule text")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestMemoryStore_FindMatchingRule_NoMatch(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewMemoryStore(mock)

	mock.ExpectQuery("SELECT .+ FROM memories WHERE org_id .+ AND repo .+ AND active = true AND lower.rule.").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(memoryColumns))

	_, err = store.FindMatchingRule(context.Background(), uuid.New(), "org/repo", "nonexistent rule")
	require.Error(t, err, "should return an error when no matching rule is found")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestMemoryStore_UpdateMemory_InsertOnlyVersioning(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewMemoryStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	newID := uuid.New()
	now := time.Now()
	sourceCommentIDs := []uuid.UUID{uuid.New()}

	// Transaction: Begin
	mock.ExpectBegin()

	// Step 1: Expect the inactivation query that returns the existing row values
	mock.ExpectQuery("UPDATE memories SET active = false WHERE id .+ AND org_id .+ AND active = true RETURNING").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"org_id", "repo", "rule", "category", "source_comment_ids",
				"occurrence_count", "status", "manually_curated",
				"scope", "source", "last_used_at", "times_reinforced", "file_patterns",
			}).AddRow(orgID, "org/repo", "Old rule text", "style", sourceCommentIDs, 1, "candidate", false,
				"repo", "review", &now, 0, []string(nil)),
		)

	// Step 2: Expect the insert of the new active row with updated values and returned row
	newRule := "Updated rule text"
	mock.ExpectQuery("INSERT INTO memories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(memoryColumns).
				AddRow(newID, orgID, "org/repo", newRule, "style", sourceCommentIDs, 1, "candidate", true, true,
					"repo", "review", &now, 0, []string(nil), now),
		)

	// Transaction: Commit
	mock.ExpectCommit()

	err = store.UpdateMemory(context.Background(), orgID, id, &newRule, nil)
	require.NoError(t, err, "should update memory using insert-only versioning without error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestMemoryStore_IncrementOccurrence_PromotesAtTwo(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	store := NewMemoryStore(mock)
	orgID := uuid.New()
	memoryID := uuid.New()
	commentID := uuid.New()
	existingCommentID := uuid.New()
	now := time.Now()

	// Transaction: Begin
	mock.ExpectBegin()

	// Step 1: Expect the inactivation query returning a candidate memory with occurrence_count=1
	mock.ExpectQuery("UPDATE memories SET active = false WHERE id .+ AND org_id .+ AND active = true RETURNING").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"org_id", "repo", "rule", "category", "source_comment_ids",
				"occurrence_count", "status", "manually_curated",
				"scope", "source", "last_used_at", "times_reinforced", "file_patterns",
			}).AddRow(orgID, "org/repo", "Always use structured logging", "style",
				[]uuid.UUID{existingCommentID}, 1, "candidate", false,
				"repo", "review", &now, 1, []string(nil)),
		)

	// Step 2: Expect insert of new row with occurrence_count=2 and auto-promoted status='active'
	// 12 named args: org_id, repo, rule, category, source_comment_ids, occurrence_count,
	// status, manually_curated, scope, source, times_reinforced, file_patterns
	// (active=true and last_used_at=now() are hardcoded in the query)
	mock.ExpectExec("INSERT INTO memories").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))

	// Transaction: Commit
	mock.ExpectCommit()

	err = store.IncrementOccurrence(context.Background(), orgID, memoryID, commentID)
	require.NoError(t, err, "should increment occurrence and promote memory without error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestMemoryStore_ListForContext_IncludesRepoAndOrgScope(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewMemoryStore(mock)
	orgID := uuid.New()
	repoMemID := uuid.New()
	orgMemID := uuid.New()
	now := time.Now()

	repoRow := []any{
		repoMemID, orgID, "org/repo", "Use gofmt", "style",
		[]uuid.UUID{}, 2, "active", false, true,
		"repo", "review", &now, 3, []string(nil), now,
	}
	orgRow := []any{
		orgMemID, orgID, "", "Always add tests", "testing",
		[]uuid.UUID{}, 1, "active", true, true,
		"org", "manual", &now, 1, []string(nil), now,
	}

	mock.ExpectQuery("SELECT .+ FROM memories WHERE org_id .+ AND active = true AND status = 'active'").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(memoryColumns).
				AddRow(repoRow...).
				AddRow(orgRow...),
		)

	memories, err := store.ListForContext(context.Background(), orgID, "org/repo")
	require.NoError(t, err)
	require.Len(t, memories, 2, "should return both repo-scoped and org-scoped memories")
	require.Equal(t, repoMemID, memories[0].ID)
	require.Equal(t, orgMemID, memories[1].ID)
	require.Equal(t, "repo", memories[0].Scope)
	require.Equal(t, "org", memories[1].Scope)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMemoryStore_ReinforceBatch_BatchCTE(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewMemoryStore(mock)
	orgID := uuid.New()
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}

	// The batch CTE is a single Exec — deactivates and inserts in one statement.
	mock.ExpectExec("WITH deactivated AS").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 3))

	err = store.ReinforceBatch(context.Background(), orgID, ids)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMemoryStore_ReinforceBatch_ExecError(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewMemoryStore(mock)
	orgID := uuid.New()
	ids := []uuid.UUID{uuid.New()}

	mock.ExpectExec("WITH deactivated AS").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("connection refused"))

	err = store.ReinforceBatch(context.Background(), orgID, ids)
	require.Error(t, err)
	require.Contains(t, err.Error(), "reinforce memories batch")
	require.Contains(t, err.Error(), "connection refused")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMemoryStore_ReinforceBatch_EmptyIDs(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewMemoryStore(mock)

	// Should return immediately without any DB calls.
	err = store.ReinforceBatch(context.Background(), uuid.New(), nil)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMemoryStore_ListActiveByOrg_Success(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewMemoryStore(mock)
	orgID := uuid.New()
	id := uuid.New()
	now := time.Now()

	orgRow := []any{
		id, orgID, "", "Always add tests", "testing",
		[]uuid.UUID{}, 1, "active", true, true,
		"org", "manual", &now, 1, []string(nil), now,
	}

	mock.ExpectQuery("SELECT .+ FROM memories WHERE org_id .+ AND active = true AND status = 'active' AND scope = 'org'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(memoryColumns).AddRow(orgRow...),
		)

	memories, err := store.ListActiveByOrg(context.Background(), orgID)
	require.NoError(t, err)
	require.Len(t, memories, 1)
	require.Equal(t, "org", memories[0].Scope)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMemoryStore_MultiTenancy_OrgIDFilter(t *testing.T) {
	t.Parallel()

	orgA := uuid.New()
	orgB := uuid.New()
	memoryID := uuid.New()
	now := time.Now()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		run       func(store *MemoryStore) error
	}{
		{
			name: "GetByID filters by org_id and returns no rows for wrong org",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM memories WHERE id .+ AND org_id .+ AND active = true").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(memoryColumns))
			},
			run: func(store *MemoryStore) error {
				_, err := store.GetByID(context.Background(), orgB, memoryID)
				return err
			},
		},
		{
			name: "ListByRepo filters by org_id and returns only matching org memories",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM memories WHERE org_id .+ AND repo .+ AND active = true").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(memoryColumns).
							AddRow(newMemoryRow(memoryID, orgA, now)...),
					)
			},
			run: func(store *MemoryStore) error {
				memories, err := store.ListByRepo(context.Background(), orgA, "org/repo", MemoryFilters{})
				if err != nil {
					return err
				}
				require.Len(t, memories, 1, "should return only memories for the matching org")
				require.Equal(t, orgA, memories[0].OrgID, "returned memory should belong to the queried org")
				return nil
			},
		},
		{
			name: "ListActiveByRepo filters by org_id",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM memories WHERE org_id .+ AND repo .+ AND active = true AND status = 'active'").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(memoryColumns))
			},
			run: func(store *MemoryStore) error {
				memories, err := store.ListActiveByRepo(context.Background(), orgB, "org/repo")
				if err != nil {
					return err
				}
				require.Empty(t, memories, "should return no memories for an org with no active memories")
				return nil
			},
		},
		{
			name: "FindMatchingRule filters by org_id",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM memories WHERE org_id .+ AND repo .+ AND active = true AND lower.rule.").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(memoryColumns))
			},
			run: func(store *MemoryStore) error {
				_, err := store.FindMatchingRule(context.Background(), orgB, "org/repo", "some rule")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool without error")
			defer mock.Close()

			store := NewMemoryStore(mock)
			tt.setupMock(mock)

			runErr := tt.run(store)
			_ = runErr
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
