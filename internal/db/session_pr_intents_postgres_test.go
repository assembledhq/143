package db

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func newSessionPRIntentPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for publication team review concurrency proofs")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err, "connect to disposable PostgreSQL")
	schema := "session_pr_intents_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create isolated publication test schema")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err, "remove isolated publication test schema")
		require.NoError(t, admin.Close(ctx), "close schema administration connection")
	})
	config, err := pgxpool.ParseConfig(databaseURL)
	require.NoError(t, err, "parse disposable PostgreSQL configuration")
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err, "create isolated publication test pool")
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `
		CREATE TABLE organizations (id uuid PRIMARY KEY);
		CREATE TABLE repositories (id uuid PRIMARY KEY, org_id uuid NOT NULL, full_name text NOT NULL);
		CREATE TABLE sessions (id uuid PRIMARY KEY, org_id uuid NOT NULL, repository_id uuid NOT NULL, UNIQUE(id, org_id));
		CREATE TABLE automation_runs (id uuid PRIMARY KEY, org_id uuid NOT NULL);
		CREATE TABLE session_automation_links (session_id uuid PRIMARY KEY, org_id uuid NOT NULL, automation_run_id uuid NOT NULL);
		CREATE TABLE session_changesets (id uuid PRIMARY KEY, org_id uuid NOT NULL, session_id uuid NOT NULL, is_primary boolean NOT NULL, UNIQUE(id, org_id, session_id));
		CREATE TABLE session_publications (id uuid PRIMARY KEY, org_id uuid NOT NULL, session_id uuid NOT NULL, changeset_id uuid NOT NULL);`)
	require.NoError(t, err, "create the real migration's scoped parent tables")
	for _, migration := range []string{"000314_session_pr_review_intent.up.sql", "000316_session_pr_intent_content.up.sql"} {
		up, err := os.ReadFile(filepath.Join("..", "..", "migrations", migration))
		require.NoError(t, err, "read publication intent migration")
		_, err = pool.Exec(ctx, "BEGIN; "+string(up)+"; COMMIT;")
		require.NoError(t, err, "apply publication intent migration")
	}
	return pool
}

type sessionPRIntentPostgresFixture struct {
	intent        models.SessionPRIntent
	repositoryID  uuid.UUID
	publicationID uuid.UUID
}

func seedSessionPRIntentPostgres(t *testing.T, pool *pgxpool.Pool) sessionPRIntentPostgresFixture {
	t.Helper()
	f := sessionPRIntentPostgresFixture{intent: newSessionPRIntentFixture(), repositoryID: uuid.New(), publicationID: uuid.New()}
	ctx := context.Background()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO organizations VALUES ($1)`, []any{f.intent.OrgID}},
		{`INSERT INTO repositories VALUES ($1, $2, 'example/project')`, []any{f.repositoryID, f.intent.OrgID}},
		{`INSERT INTO sessions VALUES ($1, $2, $3)`, []any{f.intent.SessionID, f.intent.OrgID, f.repositoryID}},
		{`INSERT INTO automation_runs VALUES ($1, $2)`, []any{f.intent.AutomationRunID, f.intent.OrgID}},
		{`INSERT INTO session_automation_links VALUES ($1, $2, $3)`, []any{f.intent.SessionID, f.intent.OrgID, f.intent.AutomationRunID}},
		{`INSERT INTO session_changesets VALUES ($1, $2, $3, true)`, []any{f.intent.ChangesetID, f.intent.OrgID, f.intent.SessionID}},
	}
	for _, statement := range statements {
		_, err := pool.Exec(ctx, statement.query, statement.args...)
		require.NoError(t, err, "seed isolated scoped publication parents")
	}
	return f
}

func seedSessionPRPublication(t *testing.T, pool *pgxpool.Pool, f sessionPRIntentPostgresFixture) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `INSERT INTO session_publications VALUES ($1, $2, $3, $4)`, f.publicationID, f.intent.OrgID, f.intent.SessionID, f.intent.ChangesetID)
	require.NoError(t, err, "seed scoped durable publication")
}

func requireSessionPRSQLState(t *testing.T, err error, expected string) {
	t.Helper()
	var postgresError *pgconn.PgError
	require.ErrorAs(t, err, &postgresError, "database constraint should reject invalid scoped state")
	require.Equal(t, expected, postgresError.Code, "database should reject the expected constraint class")
}

func TestSessionPRIntentPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*testing.T, *pgxpool.Pool, sessionPRIntentPostgresFixture)
	}{
		{"freeze retains staged teams and timestamp", func(t *testing.T, pool *pgxpool.Pool, f sessionPRIntentPostgresFixture) {
			ctx := context.Background()
			store := NewSessionPRIntentStore(pool)
			require.NoError(t, store.Stage(ctx, f.intent.OrgID, f.intent), "authorized scoped metadata should stage")
			f.intent.TeamReviewers = []string{"example/security", "example/reviewers"}
			require.NoError(t, store.Stage(ctx, f.intent.OrgID, f.intent), "unfrozen metadata may be replaced before completion")
			frozen, err := store.Freeze(ctx, f.intent.OrgID, f.intent.SessionID, f.intent.AutomationRunID, f.intent.ChangesetID)
			require.NoError(t, err, "completion should freeze staged metadata")
			require.Equal(t, f.intent.TeamReviewers, frozen.TeamReviewers, "completion should capture the latest staged teams exactly")
			require.NotNil(t, frozen.FrozenAt, "completion should persist its frozen boundary")
			replayed, err := store.Freeze(ctx, f.intent.OrgID, f.intent.SessionID, f.intent.AutomationRunID, f.intent.ChangesetID)
			require.NoError(t, err, "completion replay should reuse the frozen snapshot")
			require.Equal(t, frozen, replayed, "completion replay must not alter teams or timestamps")
			require.ErrorIs(t, store.Stage(ctx, f.intent.OrgID, f.intent), ErrSessionPRIntentFrozen, "late staging must not overwrite frozen metadata")
		}},
		{"separate staging calls compose reviewers and text", func(t *testing.T, pool *pgxpool.Pool, f sessionPRIntentPostgresFixture) {
			ctx := context.Background()
			store := NewSessionPRIntentStore(pool)
			reviewersOnly := f.intent
			reviewersOnly.Title, reviewersOnly.Body = nil, nil
			require.NoError(t, store.Stage(ctx, f.intent.OrgID, reviewersOnly), "reviewers may be staged before text")
			textOnly := f.intent
			textOnly.TeamReviewers = nil
			require.NoError(t, store.Stage(ctx, f.intent.OrgID, textOnly), "text may be staged without repeating reviewers")
			title := "docs: Final weekly report"
			titleOnly := f.intent
			titleOnly.TeamReviewers, titleOnly.Title, titleOnly.Body = nil, &title, nil
			require.NoError(t, store.Stage(ctx, f.intent.OrgID, titleOnly), "a later title replaces only the title")
			frozen, err := store.Freeze(ctx, f.intent.OrgID, f.intent.SessionID, f.intent.AutomationRunID, f.intent.ChangesetID)
			require.NoError(t, err, "completion should freeze composed metadata")
			require.Equal(t, f.intent.TeamReviewers, frozen.TeamReviewers, "text-only calls must not clear staged reviewers")
			require.Equal(t, &title, frozen.Title, "the last staged title should win")
			require.Equal(t, f.intent.Body, frozen.Body, "title-only calls must not clear the staged body")
			require.ErrorIs(t, store.Stage(ctx, f.intent.OrgID, textOnly), ErrSessionPRIntentFrozen, "late text must not overwrite frozen metadata")
		}},
		{"schema rejects malformed staged text", func(t *testing.T, pool *pgxpool.Pool, f sessionPRIntentPostgresFixture) {
			ctx := context.Background()
			cases := []struct {
				name  string
				title *string
				body  *string
			}{
				{"multiline title", new("docs:\nreport"), nil},
				{"padded title", new(" docs: report"), nil},
				{"oversized title", new(strings.Repeat("a", models.MaxStagedPRTitleBytes+1)), nil},
				{"blank body", nil, new("   ")},
				{"oversized body", nil, new(strings.Repeat("a", models.MaxStagedPRBodyLength+1))},
			}
			for _, tt := range cases {
				_, err := pool.Exec(ctx, `INSERT INTO session_pr_intents(org_id, session_id, automation_run_id, changeset_id, title, body) VALUES ($1, $2, $3, $4, $5, $6)`,
					f.intent.OrgID, f.intent.SessionID, f.intent.AutomationRunID, f.intent.ChangesetID, tt.title, tt.body)
				requireSessionPRSQLState(t, err, "23514")
			}
		}},
		{"content down migration keeps reviewer staging", func(t *testing.T, pool *pgxpool.Pool, f sessionPRIntentPostgresFixture) {
			ctx := context.Background()
			down, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000316_session_pr_intent_content.down.sql"))
			require.NoError(t, err, "read content down migration")
			_, err = pool.Exec(ctx, "BEGIN; "+string(down)+"; COMMIT;")
			require.NoError(t, err, "content rollback should apply cleanly")
			var columns int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'session_pr_intents' AND column_name IN ('title', 'body')`).Scan(&columns), "inspect content rollback")
			require.Equal(t, 0, columns, "rollback should remove staged text columns")
			_, err = pool.Exec(ctx, `INSERT INTO session_pr_intents(org_id, session_id, automation_run_id, changeset_id, team_reviewers) VALUES ($1, $2, $3, $4, $5)`,
				f.intent.OrgID, f.intent.SessionID, f.intent.AutomationRunID, f.intent.ChangesetID, f.intent.TeamReviewers)
			require.NoError(t, err, "rollback should keep reviewer staging storage intact")
		}},
		{"empty freeze fences later tool calls", func(t *testing.T, pool *pgxpool.Pool, f sessionPRIntentPostgresFixture) {
			ctx := context.Background()
			store := NewSessionPRIntentStore(pool)
			frozen, err := store.Freeze(ctx, f.intent.OrgID, f.intent.SessionID, f.intent.AutomationRunID, f.intent.ChangesetID)
			require.NoError(t, err, "completion without metadata should still create a fence")
			require.Equal(t, []string{}, frozen.TeamReviewers, "an unstaged completion should freeze an empty team list")
			require.Nil(t, frozen.Title, "an unstaged completion keeps the generated title")
			require.Nil(t, frozen.Body, "an unstaged completion keeps the generated body")
			require.ErrorIs(t, store.Stage(ctx, f.intent.OrgID, f.intent), ErrSessionPRIntentFrozen, "late tool calls must not add teams after an empty freeze")
		}},
		{"all intent scope dimensions are enforced", func(t *testing.T, pool *pgxpool.Pool, f sessionPRIntentPostgresFixture) {
			ctx := context.Background()
			other := seedSessionPRIntentPostgres(t, pool)
			secondaryID := uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO session_changesets VALUES ($1, $2, $3, false)`, secondaryID, f.intent.OrgID, f.intent.SessionID)
			require.NoError(t, err, "seed nonprimary changeset")
			cases := []struct {
				name   string
				change func(*models.SessionPRIntent)
			}{
				{"organization", func(i *models.SessionPRIntent) { i.OrgID = other.intent.OrgID }},
				{"session", func(i *models.SessionPRIntent) { i.SessionID = other.intent.SessionID }},
				{"run", func(i *models.SessionPRIntent) { i.AutomationRunID = other.intent.AutomationRunID }},
				{"changeset", func(i *models.SessionPRIntent) { i.ChangesetID = other.intent.ChangesetID }},
				{"primary changeset", func(i *models.SessionPRIntent) { i.ChangesetID = secondaryID }},
				{"repository team owner", func(i *models.SessionPRIntent) { i.TeamReviewers = []string{"other/reviewers"} }},
			}
			for _, tt := range cases {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					intent := f.intent
					tt.change(&intent)
					store := NewSessionPRIntentStore(pool)
					require.ErrorIs(t, store.Stage(ctx, intent.OrgID, intent), pgx.ErrNoRows, "cross-scope metadata must not be persisted")
					if tt.name != "repository team owner" {
						_, err := store.Freeze(ctx, intent.OrgID, intent.SessionID, intent.AutomationRunID, intent.ChangesetID)
						require.ErrorIs(t, err, pgx.ErrNoRows, "cross-scope completion must not create an intent")
					}
				})
			}
		}},
		{"existing publication prohibits new staged metadata", func(t *testing.T, pool *pgxpool.Pool, f sessionPRIntentPostgresFixture) {
			seedSessionPRPublication(t, pool, f)
			require.ErrorIs(t, NewSessionPRIntentStore(pool).Stage(context.Background(), f.intent.OrgID, f.intent), pgx.ErrNoRows, "staging must not add metadata to existing publication work")
		}},
		{"foreign keys reject malformed cross-org links", func(t *testing.T, pool *pgxpool.Pool, f sessionPRIntentPostgresFixture) {
			ctx := context.Background()
			other := seedSessionPRIntentPostgres(t, pool)
			_, err := pool.Exec(ctx, `UPDATE session_automation_links SET automation_run_id = $1 WHERE session_id = $2 AND org_id = $3`, other.intent.AutomationRunID, f.intent.SessionID, f.intent.OrgID)
			require.NoError(t, err, "simulate a preexisting malformed legacy link")
			_, err = pool.Exec(ctx, `INSERT INTO session_pr_intents(org_id, session_id, automation_run_id, changeset_id) VALUES ($1, $2, $3, $4)`, f.intent.OrgID, f.intent.SessionID, other.intent.AutomationRunID, f.intent.ChangesetID)
			requireSessionPRSQLState(t, err, "23503")
			_, err = NewSessionPRIntentStore(pool).Freeze(ctx, f.intent.OrgID, f.intent.SessionID, other.intent.AutomationRunID, f.intent.ChangesetID)
			require.ErrorIs(t, err, pgx.ErrNoRows, "store queries must reject a run from another tenant even through a malformed link")
		}},
		{"down migration restores parent schema", func(t *testing.T, pool *pgxpool.Pool, _ sessionPRIntentPostgresFixture) {
			down, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000314_session_pr_review_intent.down.sql"))
			require.NoError(t, err, "read down migration")
			_, err = pool.Exec(context.Background(), string(down))
			require.NoError(t, err, "down migration should remove child tables before parent constraints")
			var intentTable, receiptTable *string
			require.NoError(t, pool.QueryRow(context.Background(), `SELECT to_regclass('session_pr_intents')::text, to_regclass('publication_team_review_requests')::text`).Scan(&intentTable, &receiptTable), "inspect migration rollback")
			require.Nil(t, intentTable, "rollback should remove staging storage")
			require.Nil(t, receiptTable, "rollback should remove receipt storage")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newSessionPRIntentPostgres(t)
			tt.run(t, pool, seedSessionPRIntentPostgres(t, pool))
		})
	}
}

func waitSessionPRBackendBlocked(t *testing.T, pool *pgxpool.Pool, pid int) {
	t.Helper()
	require.Eventually(t, func() bool {
		var blocked bool
		err := pool.QueryRow(context.Background(), `SELECT wait_event_type = 'Lock' FROM pg_stat_activity WHERE pid = $1`, pid).Scan(&blocked)
		return err == nil && blocked
	}, 5*time.Second, 10*time.Millisecond, "concurrent operation should wait for the exact unique-row reservation lock")
}

func TestSessionPRIntentPostgresFreezeRace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		stageFirst bool
	}{
		{"staging wins before completion", true},
		{"empty completion wins before staging", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := newSessionPRIntentPostgres(t)
			f := seedSessionPRIntentPostgres(t, pool)
			first, err := pool.Begin(ctx)
			require.NoError(t, err, "begin first competing transaction")
			defer func() { _ = first.Rollback(ctx) }()
			second, err := pool.Begin(ctx)
			require.NoError(t, err, "begin second competing transaction")
			defer func() { _ = second.Rollback(ctx) }()
			var secondPID int
			require.NoError(t, second.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&secondPID), "identify exact competing backend")
			firstStore := NewSessionPRIntentStore(first)
			if tt.stageFirst {
				require.NoError(t, firstStore.Stage(ctx, f.intent.OrgID, f.intent), "stage should reserve the intent row first")
			} else {
				_, err = firstStore.Freeze(ctx, f.intent.OrgID, f.intent.SessionID, f.intent.AutomationRunID, f.intent.ChangesetID)
				require.NoError(t, err, "completion should reserve an empty frozen row first")
			}
			type result struct {
				intent models.SessionPRIntent
				err    error
			}
			done := make(chan result, 1)
			go func() {
				store := NewSessionPRIntentStore(second)
				if tt.stageFirst {
					frozen, err := store.Freeze(ctx, f.intent.OrgID, f.intent.SessionID, f.intent.AutomationRunID, f.intent.ChangesetID)
					done <- result{intent: frozen, err: err}
				} else {
					done <- result{err: store.Stage(ctx, f.intent.OrgID, f.intent)}
				}
			}()
			waitSessionPRBackendBlocked(t, pool, secondPID)
			require.NoError(t, first.Commit(ctx), "release the winning staged or frozen row")
			outcome := <-done
			if tt.stageFirst {
				require.NoError(t, outcome.err, "completion should observe the committed staged metadata after waiting")
				require.Equal(t, f.intent.TeamReviewers, outcome.intent.TeamReviewers, "completion must retain metadata staged by the winning transaction")
			} else {
				require.ErrorIs(t, outcome.err, ErrSessionPRIntentFrozen, "losing stage must observe the empty completion fence after waiting")
			}
			require.NoError(t, second.Commit(ctx), "finish the second transaction without discarding its freeze")
			frozen, err := NewSessionPRIntentStore(pool).Freeze(ctx, f.intent.OrgID, f.intent.SessionID, f.intent.AutomationRunID, f.intent.ChangesetID)
			require.NoError(t, err, "read the final immutable completion snapshot")
			expectedTeams := []string{}
			if tt.stageFirst {
				expectedTeams = f.intent.TeamReviewers
			}
			require.Equal(t, expectedTeams, frozen.TeamReviewers, "stage/freeze race must resolve to exactly the winning serialized intent")
		})
	}
}

func TestTeamReviewRequestPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*testing.T, *pgxpool.Pool, sessionPRIntentPostgresFixture)
	}{
		{"uncertainty and confirmed delivery never authorize replay", func(t *testing.T, pool *pgxpool.Pool, f sessionPRIntentPostgresFixture) {
			ctx := context.Background()
			store := NewTeamReviewRequestStore(pool)
			receipt, claimed, err := store.Begin(ctx, f.intent.OrgID, f.publicationID, "example/reviewers")
			require.NoError(t, err, "first external request should reserve durable delivery state")
			require.True(t, claimed, "new reservation should authorize one POST")
			require.Equal(t, models.TeamReviewRequestSending, receipt.State, "reservation must persist uncertainty before external delivery")
			replayed, claimed, err := store.Begin(ctx, f.intent.OrgID, f.publicationID, "example/reviewers")
			require.NoError(t, err, "uncertain delivery should return its existing reservation")
			require.False(t, claimed, "uncertain delivery must require reconciliation rather than another POST")
			require.Equal(t, receipt, replayed, "replaying uncertainty must not mutate its durable receipt")
			require.NoError(t, store.MarkRequested(ctx, f.intent.OrgID, f.publicationID, "example/reviewers"), "a confirmed POST or pending-request reconciliation should record delivery")
			require.ErrorIs(t, store.MarkRetryable(ctx, f.intent.OrgID, f.publicationID, "example/reviewers"), pgx.ErrNoRows, "late failures cannot reset confirmed delivery")
			replayed, claimed, err = store.Begin(ctx, f.intent.OrgID, f.publicationID, "example/reviewers")
			require.NoError(t, err, "confirmed receipt should survive later replays")
			require.False(t, claimed, "a completed review disappearing from pending requests must not cause a duplicate POST")
			require.Equal(t, models.TeamReviewRequestRequested, replayed.State, "confirmed delivery must remain authoritative")
		}},
		{"definitive rejection permits exactly one new claim", func(t *testing.T, pool *pgxpool.Pool, f sessionPRIntentPostgresFixture) {
			ctx := context.Background()
			store := NewTeamReviewRequestStore(pool)
			_, claimed, err := store.Begin(ctx, f.intent.OrgID, f.publicationID, "example/reviewers")
			require.NoError(t, err, "first request should reserve delivery state")
			require.True(t, claimed, "first reservation should authorize a send")
			require.NoError(t, store.MarkRetryable(ctx, f.intent.OrgID, f.publicationID, "example/reviewers"), "definitive nondelivery should permit another attempt")
			_, claimed, err = store.Begin(ctx, f.intent.OrgID, f.publicationID, "example/reviewers")
			require.NoError(t, err, "definitive nondelivery should be reclaimable")
			require.True(t, claimed, "only explicit retryable state should authorize a second attempt")
			_, claimed, err = store.Begin(ctx, f.intent.OrgID, f.publicationID, "example/reviewers")
			require.NoError(t, err, "reclaimed delivery should retain its reservation")
			require.False(t, claimed, "a second concurrent claimant must not receive authority")
		}},
		{"receipt organization publication and team isolation", func(t *testing.T, pool *pgxpool.Pool, f sessionPRIntentPostgresFixture) {
			ctx := context.Background()
			other := seedSessionPRIntentPostgres(t, pool)
			seedSessionPRPublication(t, pool, other)
			store := NewTeamReviewRequestStore(pool)
			_, claimed, err := store.Begin(ctx, other.intent.OrgID, f.publicationID, "example/reviewers")
			require.ErrorIs(t, err, pgx.ErrNoRows, "another tenant must not reserve a known publication UUID")
			require.False(t, claimed, "cross-tenant lookup must never authorize an external request")
			_, err = pool.Exec(ctx, `INSERT INTO publication_team_review_requests(org_id, publication_id, team) VALUES($1, $2, 'example/reviewers')`, other.intent.OrgID, f.publicationID)
			requireSessionPRSQLState(t, err, "23503")
			_, claimed, err = store.Begin(ctx, f.intent.OrgID, f.publicationID, "example/reviewers")
			require.NoError(t, err, "owning tenant should reserve its publication")
			require.True(t, claimed, "owning tenant should receive the new reservation")
			require.ErrorIs(t, store.MarkRequested(ctx, other.intent.OrgID, f.publicationID, "example/reviewers"), pgx.ErrNoRows, "another tenant must not acknowledge a receipt")
			require.ErrorIs(t, store.MarkRetryable(ctx, other.intent.OrgID, f.publicationID, "example/reviewers"), pgx.ErrNoRows, "another tenant must not reopen a receipt")
			require.ErrorIs(t, store.MarkRequested(ctx, f.intent.OrgID, other.publicationID, "example/reviewers"), pgx.ErrNoRows, "another publication must not acknowledge a receipt")
			require.ErrorIs(t, store.MarkRequested(ctx, f.intent.OrgID, f.publicationID, "example/security"), pgx.ErrNoRows, "another team must not acknowledge a receipt")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newSessionPRIntentPostgres(t)
			f := seedSessionPRIntentPostgres(t, pool)
			seedSessionPRPublication(t, pool, f)
			tt.run(t, pool, f)
		})
	}
}

func TestTeamReviewRequestPostgresConcurrentClaim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		retryable bool
	}{
		{"first reservation", false},
		{"explicit retry after nondelivery", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := newSessionPRIntentPostgres(t)
			f := seedSessionPRIntentPostgres(t, pool)
			seedSessionPRPublication(t, pool, f)
			if tt.retryable {
				store := NewTeamReviewRequestStore(pool)
				_, claimed, err := store.Begin(ctx, f.intent.OrgID, f.publicationID, "example/reviewers")
				require.NoError(t, err, "seed the initially rejected reservation")
				require.True(t, claimed, "initial reservation should authorize a send")
				require.NoError(t, store.MarkRetryable(ctx, f.intent.OrgID, f.publicationID, "example/reviewers"), "record definitive nondelivery before the retry race")
			}
			first, err := pool.Begin(ctx)
			require.NoError(t, err, "begin winning reservation")
			defer func() { _ = first.Rollback(ctx) }()
			_, claimed, err := NewTeamReviewRequestStore(first).Begin(ctx, f.intent.OrgID, f.publicationID, "example/reviewers")
			require.NoError(t, err, "first claimant should reserve the unique delivery row")
			require.True(t, claimed, "first claimant should receive send authority")
			second, err := pool.Begin(ctx)
			require.NoError(t, err, "begin competing reservation")
			defer func() { _ = second.Rollback(ctx) }()
			var secondPID int
			require.NoError(t, second.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&secondPID), "identify competing reservation backend")
			type result struct {
				receipt models.TeamReviewRequestReceipt
				claimed bool
				err     error
			}
			done := make(chan result, 1)
			go func() {
				receipt, claimed, err := NewTeamReviewRequestStore(second).Begin(ctx, f.intent.OrgID, f.publicationID, "example/reviewers")
				done <- result{receipt: receipt, claimed: claimed, err: err}
			}()
			waitSessionPRBackendBlocked(t, pool, secondPID)
			require.NoError(t, first.Commit(ctx), "commit the reservation before external delivery")
			outcome := <-done
			require.NoError(t, outcome.err, "losing claimant should read the committed receipt with a fresh statement")
			require.False(t, outcome.claimed, "concurrent unique-key loser must not receive send authority")
			require.Equal(t, models.TeamReviewRequestSending, outcome.receipt.State, "loser should observe the durable uncertain reservation")
			require.NoError(t, second.Commit(ctx), "close read-only losing transaction")
		})
	}
}
