package db

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestPromptRecordCompatibilityMigrationPostgres(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		table     string
		keyColumn string
	}{
		{name: "current writer", table: "code_review_prompt_records", keyColumn: "record_key"},
		{name: "legacy writer", table: "code_review_prompt_artifacts", keyColumn: "artifact_key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			conn := newPromptRecordCompatibilityTestConn(t)
			applyPromptRecordCompatibilityTestMigration(t, conn, "000280_rename_legacy_output_terms.up.sql")

			orgID, sessionID := uuid.New(), uuid.New()
			_, err := conn.Exec(ctx, `INSERT INTO organizations (id) VALUES ($1)`, orgID)
			require.NoError(t, err, "test should create the prompt organization")
			_, err = conn.Exec(ctx, `INSERT INTO sessions (id, org_id) VALUES ($1, $2)`, sessionID, orgID)
			require.NoError(t, err, "test should create the review session")

			expected := models.CodeReviewPromptRecord{
				ID: uuid.New(), OrgID: orgID, SessionID: sessionID,
				RecordKey: "reviewer-04-codex", Role: "reviewer", AgentProvider: "codex",
				Content: "initial prompt", Metadata: json.RawMessage(`{"attempt": 0}`),
				CreatedAt: time.Date(2026, 9, 2, 18, 0, 0, 0, time.UTC),
			}
			upsertSQL := fmt.Sprintf(`
				INSERT INTO %s (id, org_id, session_id, %s, role, agent_provider, content, metadata, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
				ON CONFLICT (org_id, %s) DO UPDATE
				SET content = EXCLUDED.content, metadata = EXCLUDED.metadata`, tt.table, tt.keyColumn, tt.keyColumn)
			_, err = conn.Exec(ctx, upsertSQL, expected.ID, orgID, sessionID, expected.RecordKey,
				expected.Role, expected.AgentProvider, expected.Content, expected.Metadata, expected.CreatedAt)
			require.NoError(t, err, "initial prompt should be writable by either generation")
			requirePromptRecordCompatibilityRows(t, conn, orgID, []models.CodeReviewPromptRecord{expected})

			applyPromptRecordCompatibilityTestMigration(t, conn, "000288_fix_code_review_prompt_record_compatibility.up.sql")
			expected.Content = "retry prompt"
			expected.Metadata = json.RawMessage(`{"attempt": 1}`)
			if tt.table == "code_review_prompt_records" {
				// Exercise the worker's actual retry path, including its conflict update.
				retry := models.CodeReviewPromptRecord{
					OrgID: orgID, SessionID: sessionID, RecordKey: expected.RecordKey,
					Role: expected.Role, AgentProvider: expected.AgentProvider,
					Content: expected.Content, Metadata: expected.Metadata,
				}
				err = NewCodeReviewStore(conn).CreatePromptRecord(ctx, &retry)
				require.NoError(t, err, "review retries should upsert an existing prompt on an upgraded database")
				retry.CreatedAt = retry.CreatedAt.UTC()
				require.Equal(t, expected, retry, "retry should preserve the prompt identity while updating its contents")
			} else {
				_, err = conn.Exec(ctx, upsertSQL, uuid.New(), orgID, sessionID, expected.RecordKey,
					expected.Role, expected.AgentProvider, expected.Content, expected.Metadata, expected.CreatedAt)
				require.NoError(t, err, "draining workers should still upsert legacy prompt rows")
			}
			requirePromptRecordCompatibilityRows(t, conn, orgID, []models.CodeReviewPromptRecord{expected})

			applyPromptRecordCompatibilityTestMigration(t, conn, "000288_fix_code_review_prompt_record_compatibility.down.sql")
			// Rolling back the repair must keep both worker generations writable.
			_, err = conn.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s = $1 WHERE org_id = $2 AND %s = $3`,
				tt.table, tt.keyColumn, tt.keyColumn), "renamed-prompt", orgID, expected.RecordKey)
			require.NoError(t, err, "rekeying either generation should retire the old counterpart without a primary-key collision")
			expected.RecordKey = "renamed-prompt"
			requirePromptRecordCompatibilityRows(t, conn, orgID, []models.CodeReviewPromptRecord{expected})

			_, err = conn.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE org_id = $1 AND %s = $2`,
				tt.table, tt.keyColumn), orgID, expected.RecordKey)
			require.NoError(t, err, "deleting either generation should remove its synchronized counterpart")
			requirePromptRecordCompatibilityRows(t, conn, orgID, []models.CodeReviewPromptRecord{})
		})
	}
}

func TestPromptRecordCompatibilityMigrationFreshSchemaPostgres(t *testing.T) {
	t.Parallel()

	conn := newPromptRecordCompatibilityTestConn(t)
	_, err := conn.Exec(context.Background(), `
		ALTER TABLE code_review_prompt_artifacts RENAME TO code_review_prompt_records;
		ALTER TABLE code_review_prompt_records RENAME COLUMN artifact_key TO record_key;
	`)
	require.NoError(t, err, "test should model a fresh schema containing only the current prompt table")

	applyPromptRecordCompatibilityTestMigration(t, conn, "000288_fix_code_review_prompt_record_compatibility.up.sql")
	applyPromptRecordCompatibilityTestMigration(t, conn, "000288_fix_code_review_prompt_record_compatibility.down.sql")

	var currentTable string
	var legacyTable, compatibilityFunction *string
	err = conn.QueryRow(context.Background(), `
		SELECT to_regclass('code_review_prompt_records')::text,
		       to_regclass('code_review_prompt_artifacts')::text,
		       to_regprocedure('sync_code_review_prompt_record_compatibility()')::text
	`).Scan(&currentTable, &legacyTable, &compatibilityFunction)
	require.NoError(t, err, "test should inspect the fresh schema after applying and rolling back the repair")
	require.Equal(t, "code_review_prompt_records", currentTable, "the current prompt table should remain available")
	require.Nil(t, legacyTable, "fresh installations should not acquire a legacy prompt table")
	require.Nil(t, compatibilityFunction, "fresh installations should not acquire an unused compatibility function")
}

func newPromptRecordCompatibilityTestConn(t *testing.T) *pgx.Conn {
	t.Helper()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run the prompt compatibility migration tests")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err, "test should connect to TEST_DATABASE_URL")
	t.Cleanup(func() {
		require.NoError(t, conn.Close(context.Background()), "test should close its PostgreSQL connection")
	})

	schema := "test_prompt_compatibility_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	_, err = conn.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "test should create an isolated migration schema")
	t.Cleanup(func() {
		_, cleanupErr := conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		require.NoError(t, cleanupErr, "test should remove its isolated migration schema")
	})
	_, err = conn.Exec(ctx, `SET search_path TO `+schema)
	require.NoError(t, err, "test should isolate migration objects")
	_, err = conn.Exec(ctx, legacyOutputNameTestSchema)
	require.NoError(t, err, "test should create the deployed legacy schema")
	return conn
}

func applyPromptRecordCompatibilityTestMigration(t *testing.T, conn *pgx.Conn, name string) {
	t.Helper()

	body, err := os.ReadFile("../../migrations/" + name)
	require.NoError(t, err, "test should read migration %s", name)
	_, err = conn.Exec(context.Background(), string(body))
	require.NoError(t, err, "test should apply migration %s", name)
}

func requirePromptRecordCompatibilityRows(t *testing.T, conn *pgx.Conn, orgID uuid.UUID, expected []models.CodeReviewPromptRecord) {
	t.Helper()

	tables := []struct{ name, key string }{
		{name: "code_review_prompt_records", key: "record_key"},
		{name: "code_review_prompt_artifacts", key: "artifact_key"},
	}
	for _, table := range tables {
		rows, err := conn.Query(context.Background(), fmt.Sprintf(`
			SELECT id, org_id, session_id, %s AS record_key, role, agent_provider, content, metadata, created_at
			FROM %s WHERE org_id = $1 ORDER BY %s`, table.key, table.name, table.key), orgID)
		require.NoError(t, err, "test should read synchronized rows from %s", table.name)
		actual, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.CodeReviewPromptRecord])
		require.NoError(t, err, "test should decode synchronized prompt rows")
		for i := range actual {
			actual[i].CreatedAt = actual[i].CreatedAt.UTC()
		}
		require.Equal(t, expected, actual, "%s should contain the exact synchronized prompt rows", table.name)
	}
}
