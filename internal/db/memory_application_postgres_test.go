package db

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/feedback"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func newMemoryApplicationPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL for feedback persistence proof")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "connect disposable feedback database")
	schema := "feedback_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+schema)
	require.NoError(t, err, "create isolated feedback schema")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		require.NoError(t, err, "drop isolated feedback schema")
		require.NoError(t, admin.Close(ctx), "close feedback fixture connection")
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err, "parse feedback fixture DSN")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "20000"
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "open isolated feedback pool")
	t.Cleanup(pool.Close)
	maintenanceExec(t, pool, `CREATE TABLE organizations(id uuid PRIMARY KEY); CREATE TABLE pull_requests(id uuid PRIMARY KEY, org_id uuid NOT NULL REFERENCES organizations(id));`)
	for _, migration := range []string{"000006_review_feedback.up.sql", "000021_rename_review_patterns_to_memories.up.sql", "000308_review_comment_memory_application.up.sql"} {
		maintenanceExec(t, pool, maintenanceMigration(t, migration))
	}
	maintenanceExec(t, pool, `ALTER TABLE review_comments ADD COLUMN reviewer_type text NOT NULL DEFAULT ''`)
	checks := maintenanceMigration(t, "000042_schema_hardening.up.sql")
	start := strings.Index(checks, "ALTER TABLE review_comments")
	end := strings.Index(checks[start:], "ALTER TABLE review_comments VALIDATE CONSTRAINT chk_review_comments_category;")
	require.GreaterOrEqual(t, start, 0, "real category constraint exists")
	require.GreaterOrEqual(t, end, 0, "real category constraint validation exists")
	maintenanceExec(t, pool, checks[start:start+end]+"ALTER TABLE review_comments VALIDATE CONSTRAINT chk_review_comments_category;")
	return pool
}

func seedMemoryApplicationComment(t *testing.T, pool *pgxpool.Pool, orgID uuid.UUID, rule string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	prID, commentID := uuid.New(), uuid.New()
	maintenanceExec(t, pool, `INSERT INTO organizations(id) VALUES($1) ON CONFLICT DO NOTHING`, orgID)
	maintenanceExec(t, pool, `INSERT INTO pull_requests(id,org_id) VALUES($1,$2)`, prID, orgID)
	maintenanceExec(t, pool, `INSERT INTO review_comments(id,pull_request_id,org_id,github_comment_id,reviewer,body,filter_status,actionable,generalizable,generalized_rule,category)
 VALUES($1,$2,$3,1,'human','Always check errors before using a result.','accepted',true,true,$4,'logic_bug')`, commentID, prID, orgID, rule)
	require.NoError(t, pool.Ping(ctx), "seeded tenant should be queryable")
	return commentID
}

func TestMemoryApplicationPostgresDistinctSourcesAndConcurrency(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		distinct   int
		concurrent bool
	}{
		{name: "same source retry", distinct: 1},
		{name: "two punctuated sources promote", distinct: 2},
		{name: "concurrent duplicate sources", distinct: 1, concurrent: true},
		{name: "concurrent distinct sources", distinct: 2, concurrent: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newMemoryApplicationPostgres(t)
			orgID := uuid.New()
			rules := []string{"Always check errors.", "always CHECK errors."}
			ids := make([]uuid.UUID, tt.distinct)
			for i := range ids {
				ids[i] = seedMemoryApplicationComment(t, pool, orgID, rules[i])
			}
			store := NewMemoryStore(pool)
			calls := tt.distinct * 4
			outcomes := make(chan error, calls)
			var group sync.WaitGroup
			for i := 0; i < calls; i++ {
				index := i % tt.distinct
				apply := func() {
					outcomes <- store.ApplyReviewComment(context.Background(), orgID, ids[index], "org/repo", rules[index], "logic_bug")
				}
				if tt.concurrent {
					group.Go(apply)
				} else {
					apply()
				}
			}
			group.Wait()
			close(outcomes)
			for err := range outcomes {
				require.NoError(t, err, "every duplicate or distinct source should safely apply")
			}
			memories, err := store.ListByRepo(context.Background(), orgID, "org/repo", MemoryFilters{})
			require.NoError(t, err, "read final insert-only memory version")
			require.Len(t, memories, 1, "punctuation and case must resolve to one logical rule")
			memory := memories[0]
			require.Equal(t, tt.distinct, memory.OccurrenceCount, "count distinct comments exactly once")
			require.ElementsMatch(t, ids, memory.SourceCommentIDs, "retain each distinct comment once")
			status := "candidate"
			if tt.distinct == 2 {
				status = "active"
			}
			require.Equal(t, status, memory.Status, "promote only on two independent comments")
			var versions, receipts int
			require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM memories WHERE org_id=$1`, orgID).Scan(&versions), "count historical versions")
			require.NoError(t, pool.QueryRow(context.Background(), `SELECT count(*) FROM review_comments WHERE org_id=$1 AND memory_applied_at IS NOT NULL`, orgID).Scan(&receipts), "count completion markers")
			require.Equal(t, tt.distinct, versions, "each distinct source adds exactly one insert-only version")
			require.Equal(t, tt.distinct, receipts, "each source has durable completed progress")
		})
	}
}

type memoryLostCommitPool struct{ *pgxpool.Pool }

func (p memoryLostCommitPool) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return memoryLostCommitTx{tx}, nil
}

type memoryLostCommitTx struct{ pgx.Tx }

func (tx memoryLostCommitTx) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return errors.New("lost commit response")
}

func TestMemoryApplicationPostgresFailureRecovery(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name                 string
		existing, lostCommit bool
	}{
		{name: "create rolls back when progress write fails"},
		{name: "increment rolls back when progress write fails", existing: true},
		{name: "retry after successful commit response is lost", lostCommit: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := newMemoryApplicationPostgres(t)
			orgID := uuid.New()
			store := NewMemoryStore(pool)
			rule := "Always check errors."
			prior := 0
			if tt.existing {
				id := seedMemoryApplicationComment(t, pool, orgID, rule)
				require.NoError(t, store.ApplyReviewComment(ctx, orgID, id, "org/repo", rule, "logic_bug"), "seed first accepted memory")
				prior = 1
			}
			commentID := seedMemoryApplicationComment(t, pool, orgID, rule)
			var attempt *MemoryStore
			if tt.lostCommit {
				attempt = NewMemoryStore(memoryLostCommitPool{pool})
			} else {
				maintenanceExec(t, pool, `CREATE FUNCTION fail_memory_progress() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic progress write failure'; END $$;
 CREATE TRIGGER fail_memory_progress BEFORE UPDATE OF memory_applied_at ON review_comments FOR EACH ROW EXECUTE FUNCTION fail_memory_progress();`)
				attempt = store
			}
			require.Error(t, attempt.ApplyReviewComment(ctx, orgID, commentID, "org/repo", rule, "logic_bug"), "injected persistence failure must propagate")
			if !tt.lostCommit {
				var versions int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM memories WHERE org_id=$1`, orgID).Scan(&versions), "read versions after failed atomic progress write")
				require.Equal(t, prior, versions, "failed completion marker must roll back the memory write")
				maintenanceExec(t, pool, `DROP TRIGGER fail_memory_progress ON review_comments`)
			}
			require.NoError(t, store.ApplyReviewComment(ctx, orgID, commentID, "org/repo", rule, "logic_bug"), "retry accepted comment after transient failure or ambiguous commit")
			require.NoError(t, store.ApplyReviewComment(ctx, orgID, commentID, "org/repo", rule, "logic_bug"), "additional redelivery remains idempotent")
			memories, err := store.ListByRepo(ctx, orgID, "org/repo", MemoryFilters{})
			require.NoError(t, err, "load recovered memory")
			require.Len(t, memories, 1, "recovery preserves one active version")
			require.Equal(t, prior+1, memories[0].OccurrenceCount, "recovery adds the new source exactly once")
			var applied bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT memory_applied_at IS NOT NULL FROM review_comments WHERE id=$1 AND org_id=$2`, commentID, orgID).Scan(&applied), "read recovered progress")
			require.True(t, applied, "retry persists durable completion")
		})
	}
}

func TestMemoryApplicationPostgresPreservesHumanChoicesAndTenancy(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, status                  string
		curated, historical, otherOrg bool
	}{
		{name: "dismissed memory", status: "dismissed"},
		{name: "curated memory", status: "candidate", curated: true},
		{name: "historically applied source after manual rename", status: "candidate", curated: true, historical: true},
		{name: "another tenant cannot apply the source", status: "candidate", otherOrg: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := newMemoryApplicationPostgres(t)
			orgID := uuid.New()
			rule := "Always check errors."
			commentID := seedMemoryApplicationComment(t, pool, orgID, rule)
			memory := &models.Memory{OrgID: orgID, Repo: "org/repo", Rule: rule, Category: "logic_bug", Status: tt.status, ManuallyCurated: tt.curated, OccurrenceCount: 1, SourceCommentIDs: []uuid.UUID{uuid.New()}}
			if tt.historical {
				memory.Rule = "Human refined error policy."
				memory.SourceCommentIDs = []uuid.UUID{commentID}
			}
			store := NewMemoryStore(pool)
			require.NoError(t, store.Create(ctx, memory), "seed existing human memory")
			before, err := store.ListByRepo(ctx, orgID, "org/repo", MemoryFilters{})
			require.NoError(t, err, "snapshot human choice")
			caller := orgID
			if tt.otherOrg {
				caller = uuid.New()
			}
			err = store.ApplyReviewComment(ctx, caller, commentID, "org/repo", rule, "logic_bug")
			if tt.otherOrg {
				require.ErrorIs(t, err, pgx.ErrNoRows, "tenant boundary rejects another organization's comment")
			} else {
				require.NoError(t, err, "respect prior human decision while completing source")
			}
			after, err := store.ListByRepo(ctx, orgID, "org/repo", MemoryFilters{})
			require.NoError(t, err, "read retained human memory")
			require.Equal(t, before, after, "automatic feedback must preserve all human-curated memory fields")
		})
	}
}

type feedbackClassificationJSON string

func (s feedbackClassificationJSON) Complete(context.Context, string, string) (string, error) {
	return string(s), nil
}
func TestMemoryApplicationPostgresClassificationContract(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, response string
		invalid        bool
	}{
		{name: "null nonactionable category", response: `{"actionable":false,"category":null,"generalizable":true,"generalized_rule":"Do not learn this."}`},
		{name: "missing nonactionable category", response: `{"actionable":false}`},
		{name: "unknown nonactionable category", response: `{"actionable":false,"category":"praise"}`},
		{name: "unknown actionable category", response: `{"actionable":true,"category":"praise","generalizable":true,"generalized_rule":"Do not learn this."}`, invalid: true},
		{name: "null actionable category", response: `{"actionable":true,"category":null}`, invalid: true},
		{name: "missing actionable category", response: `{"actionable":true}`, invalid: true},
		{name: "empty generalizable rule", response: `{"actionable":true,"category":"logic_bug","generalizable":true,"generalized_rule":"  "}`, invalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := newMemoryApplicationPostgres(t)
			orgID := uuid.New()
			id := seedMemoryApplicationComment(t, pool, orgID, "Always check errors.")
			maintenanceExec(t, pool, `UPDATE review_comments SET filter_status='pending',category=NULL,actionable=false,generalizable=false,generalized_rule=NULL WHERE id=$1 AND org_id=$2`, id, orgID)
			comments := NewReviewCommentStore(pool)
			svc := feedback.NewService(comments, NewMemoryStore(pool), nil, feedbackClassificationJSON(tt.response), zerolog.Nop())
			err := svc.ProcessComment(ctx, id, orgID)
			if tt.invalid {
				require.Error(t, err, "malformed actionable classifications remain retryable")
			} else {
				require.NoError(t, err, "nonactionable classification uses nullable database category")
			}
			got, err := comments.GetByID(ctx, orgID, id)
			require.NoError(t, err, "read persisted classification")
			status := "filtered_not_actionable"
			if tt.invalid {
				status = "pending"
			}
			require.Equal(t, status, got.FilterStatus, "classification must retain the correct durable status")
			require.Nil(t, got.Category, "unknown categories must never violate the database enum contract")
			require.False(t, got.Generalizable, "malformed or nonactionable content must not teach a memory")
			require.Nil(t, got.GeneralizedRule, "filtered content must have no learned rule")
		})
	}
}

func TestMemoryApplicationPostgresClassificationFirstWriterWins(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newMemoryApplicationPostgres(t)
	orgID := uuid.New()
	rule, category, summary := "Always check errors.", "logic_bug", "Check errors"
	commentID := seedMemoryApplicationComment(t, pool, orgID, rule)
	maintenanceExec(t, pool, `UPDATE review_comments SET filter_status = 'pending' WHERE id = $1 AND org_id = $2`, commentID, orgID)
	comments := NewReviewCommentStore(pool)
	require.NoError(t, comments.UpdateClassification(ctx, orgID, commentID, "accepted", &category, true, true, &rule, &summary), "first classifier should persist its accepted result")
	before, err := comments.GetByID(ctx, orgID, commentID)
	require.NoError(t, err, "read the winning classification")
	require.NoError(t, comments.UpdateClassification(ctx, orgID, commentID, "filtered_not_actionable", nil, false, false, nil, nil), "stale classifier should safely no-op")
	after, err := comments.GetByID(ctx, orgID, commentID)
	require.NoError(t, err, "read classification after a competing writer")
	require.Equal(t, before, after, "a competing classifier must not overwrite durable accepted work before application")
	require.NoError(t, NewMemoryStore(pool).ApplyReviewComment(ctx, orgID, commentID, "org/repo", rule, category), "apply the winning classification exactly once")
}
