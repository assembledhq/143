package db

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestSessionStore_TerminalCleanupPostgres(t *testing.T) {
	t.Parallel()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for cleanup pagination and payload isolation proof")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err, "connect to disposable PostgreSQL")
	schema := "stream_cleanup_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "create an isolated schema for parallel cleanup testing")
	cfg, err := pgxpool.ParseConfig(databaseURL)
	require.NoError(t, err, "parse the disposable PostgreSQL connection")
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err, "create a schema-scoped pool")
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, err, "remove only this test's isolated schema")
		admin.Close()
	})
	_, err = pool.Exec(ctx, `CREATE TABLE sessions (
		id uuid PRIMARY KEY, org_id uuid NOT NULL, status text NOT NULL,
		completed_at timestamptz, deleted_at timestamptz,
		diff text, diff_history jsonb, result_summary text
	)`)
	require.NoError(t, err, "create minimal session rows including large payload columns")
	before := time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)
	completed := before.Add(-time.Hour)
	orgA, orgB := uuid.New(), uuid.New()
	_, err = pool.Exec(ctx, `INSERT INTO sessions (id, org_id, status, completed_at, deleted_at)
		SELECT lpad(to_hex(n),32,'0')::uuid,
		       CASE WHEN n % 2 = 0 THEN $1::uuid ELSE $2::uuid END,
		       (ARRAY['completed','failed','cancelled','pr_created','skipped'])[1 + n % 5],
		       CASE WHEN n = 1 THEN $3::timestamptz + interval '1 second' ELSE $3::timestamptz END,
		       CASE WHEN n = 1 THEN $3::timestamptz ELSE NULL END
		FROM generate_series(1,601) n`, orgA, orgB, completed)
	require.NoError(t, err, "seed more than one page across tenants, all terminal statuses and a soft-deleted row")
	_, err = pool.Exec(ctx, `INSERT INTO sessions (id,org_id,status,completed_at)
		SELECT lpad(to_hex(700+n),32,'0')::uuid,$1,
		       (ARRAY['pending','running','idle','awaiting_input','needs_human_guidance'])[n],$2
		FROM generate_series(1,5) n;
	`, orgA, completed)
	require.NoError(t, err, "seed nonterminal sessions that must remain ineligible")
	_, err = pool.Exec(ctx, `INSERT INTO sessions (id,org_id,status,completed_at)
		VALUES ('00000000-0000-0000-0000-000000000800',$1,'completed',NULL),
		       ('00000000-0000-0000-0000-000000000801',$1,'completed',$2),
		       ('00000000-0000-0000-0000-000000000802',$1,'completed',$2::timestamptz+interval '1 second')`, orgA, before)
	require.NoError(t, err, "seed missing and too-recent completion times")
	const payloadBytes = 4 * 1024 * 1024
	_, err = pool.Exec(ctx, `UPDATE sessions SET diff=repeat('x',$1), diff_history=jsonb_build_object('diff',repeat('y',$1))
		WHERE id='00000000-0000-0000-0000-000000000001' AND org_id=$2`, payloadBytes, orgB)
	require.NoError(t, err, "seed an oversized historical session without transferring its payload to the test")
	store := NewSessionStore(pool)
	var cursor *models.SessionStreamCleanupCursor
	for _, page := range []struct {
		start        int
		end          int
		includeLater bool
	}{
		{start: 2, end: 501},
		{start: 502, end: 601, includeLater: true},
		{start: 602, end: 601},
	} {
		expected := []models.SessionStreamCleanupCursor{}
		for n := page.start; n <= page.end; n++ {
			expected = append(expected, models.SessionStreamCleanupCursor{
				ID: uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012x", n)), CompletedAt: completed,
			})
		}
		if page.includeLater {
			expected = append(expected, models.SessionStreamCleanupCursor{
				ID: uuid.MustParse("00000000-0000-0000-0000-000000000001"), CompletedAt: completed.Add(time.Second),
			})
		}
		got, err := store.ListTerminalEndedBefore(ctx, before, cursor, 500)
		require.NoError(t, err, "cleanup should page using only IDs and completion times")
		for i := range got {
			// pgx can decode UTC instants using a fixed-offset Location.
			got[i].CompletedAt = got[i].CompletedAt.UTC()
		}
		require.Equal(t, expected, got, "equal-timestamp pagination must neither skip nor repeat a session across tenants")
		if len(got) > 0 {
			last := got[len(got)-1]
			cursor = &last
		}
	}
	var diffLength, historyLength int
	err = pool.QueryRow(ctx, `SELECT octet_length(diff),octet_length(diff_history->>'diff') FROM sessions
		WHERE id='00000000-0000-0000-0000-000000000001' AND org_id=$1`, orgB).Scan(&diffLength, &historyLength)
	require.NoError(t, err, "verify cleanup querying did not mutate historical payloads")
	require.Equal(t, []int{payloadBytes, payloadBytes}, []int{diffLength, historyLength}, "large diffs must remain intact while cleanup reads only identifiers")
}
