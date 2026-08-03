package db

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestRenameLegacyOutputTermsMigrationPostgresBehavior(t *testing.T) {
	t.Parallel()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run the output-name migration behavior test")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL)
	require.NoError(t, err, "test should connect to TEST_DATABASE_URL")
	defer func() {
		require.NoError(t, conn.Close(context.Background()), "test should close the PostgreSQL connection")
	}()

	schema := "test_output_names_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	_, err = conn.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "test should create an isolated migration schema")
	defer func() {
		_, cleanupErr := conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		require.NoError(t, cleanupErr, "test should remove the isolated migration schema")
	}()
	_, err = conn.Exec(ctx, `SET search_path TO `+schema+`, public`)
	require.NoError(t, err, "test should isolate migration objects")

	_, err = conn.Exec(ctx, legacyOutputNameTestSchema)
	require.NoError(t, err, "test should create the deployed pre-migration schema")

	orgID := uuid.New()
	sessionID := uuid.New()
	legacySeeds := []struct {
		name  string
		query string
		args  []any
	}{
		{name: "organization", query: `INSERT INTO organizations (id) VALUES ($1)`, args: []any{orgID}},
		{name: "session", query: `INSERT INTO sessions (id, org_id) VALUES ($1, $2)`, args: []any{sessionID, orgID}},
		{
			name: "prompt record",
			query: `INSERT INTO code_review_prompt_artifacts
				(id, org_id, session_id, artifact_key, role, content)
				VALUES ($1, $2, $3, 'prompt-before', 'reviewer', 'before')`,
			args: []any{uuid.New(), orgID, sessionID},
		},
		{name: "prompt reference", query: `INSERT INTO code_review_session_metadata (id, prompt_artifact_key) VALUES ($1, 'prompt-before')`, args: []any{uuid.New()}},
		{
			name:  "verification run",
			query: `INSERT INTO preview_verification_runs (id, steps, artifacts) VALUES ($1, '[{"index":1,"artifact":{"id":"capture-before"}}]', '[{"id":"capture-before"}]')`,
			args:  []any{uuid.New()},
		},
		{
			name: "review bundle",
			query: `INSERT INTO session_diff_snapshots
				(id, org_id, session_id, review_artifact_key, review_artifact_version, review_artifact_file_count)
				VALUES ($1, $2, $3, 'review-before', 1, 3)`,
			args: []any{uuid.New(), orgID, sessionID},
		},
		{name: "dependency cache", query: `INSERT INTO preview_dependency_cache (id, cache_kind, metadata) VALUES ($1, 'install_artifact', '{"kind":"install_artifact"}')`, args: []any{uuid.New()}},
		{name: "cache location", query: `INSERT INTO preview_dependency_cache_locations (id, cache_kind) VALUES ($1, 'build_artifact')`, args: []any{uuid.New()}},
		{
			name:  "agent result",
			query: `INSERT INTO code_review_agent_results (id, structured_result) VALUES ($1, '{"prompt_artifact_key":"prompt-before","raw_artifact_key":"raw-before"}')`,
			args:  []any{uuid.New()},
		},
	}
	for _, seed := range legacySeeds {
		_, err = conn.Exec(ctx, seed.query, seed.args...)
		require.NoError(t, err, "test should seed legacy %s", seed.name)
	}

	upBody, err := os.ReadFile("../../migrations/000280_rename_legacy_output_terms.up.sql")
	require.NoError(t, err, "test should read the output-name up migration")
	_, err = conn.Exec(ctx, string(upBody))
	require.NoError(t, err, "output-name up migration should apply to a deployed schema")

	var count int
	err = conn.QueryRow(ctx, `SELECT count(*) FROM code_review_prompt_records WHERE record_key = 'prompt-before'`).Scan(&count)
	require.NoError(t, err, "migrated prompt record should be queryable")
	require.Equal(t, 1, count, "migration should copy existing prompt records")

	promptWriterSeeds := []struct {
		name  string
		query string
		args  []any
	}{
		{
			name: "legacy writer",
			query: `INSERT INTO code_review_prompt_artifacts
				(id, org_id, session_id, artifact_key, role, content)
				VALUES ($1, $2, $3, 'prompt-from-old', 'reviewer', 'old writer')`,
			args: []any{uuid.New(), orgID, sessionID},
		},
		{
			name: "current writer",
			query: `INSERT INTO code_review_prompt_records
				(id, org_id, session_id, record_key, role, content)
				VALUES ($1, $2, $3, 'prompt-from-new', 'reviewer', 'new writer')`,
			args: []any{uuid.New(), orgID, sessionID},
		},
	}
	for _, seed := range promptWriterSeeds {
		_, err = conn.Exec(ctx, seed.query, seed.args...)
		require.NoError(t, err, "the %s should remain writable", seed.name)
	}
	err = conn.QueryRow(ctx, `
		SELECT count(*)
		FROM code_review_prompt_records
		WHERE record_key IN ('prompt-from-old', 'prompt-from-new')`).Scan(&count)
	require.NoError(t, err, "new prompt table should include both generations")
	require.Equal(t, 2, count, "old prompt writes should synchronize into the new table")
	err = conn.QueryRow(ctx, `SELECT count(*) FROM code_review_prompt_artifacts WHERE artifact_key = 'prompt-from-new'`).Scan(&count)
	require.NoError(t, err, "legacy prompt table should include new writes")
	require.Equal(t, 1, count, "new prompt writes should synchronize into the legacy table")

	// A rekey must retire the counterpart filed under the previous key; keeping
	// it would make the synchronizing insert collide on the primary key.
	_, err = conn.Exec(ctx, `UPDATE code_review_prompt_artifacts SET artifact_key = 'prompt-from-old-rekeyed' WHERE artifact_key = 'prompt-from-old'`)
	require.NoError(t, err, "rekeying a synchronized prompt row should not collide with its counterpart")
	err = conn.QueryRow(ctx, `SELECT count(*) FROM code_review_prompt_records WHERE record_key = 'prompt-from-old-rekeyed'`).Scan(&count)
	require.NoError(t, err, "rekeyed prompt record should be queryable")
	require.Equal(t, 1, count, "rekeyed prompt writes should synchronize into the new table")
	err = conn.QueryRow(ctx, `SELECT count(*) FROM code_review_prompt_records WHERE record_key = 'prompt-from-old'`).Scan(&count)
	require.NoError(t, err, "superseded prompt key should be queryable")
	require.Equal(t, 0, count, "the superseded key should not linger in the synchronized table")

	var promptRecordKey, promptArtifactKey string
	err = conn.QueryRow(ctx, `SELECT prompt_record_key, prompt_artifact_key FROM code_review_session_metadata`).Scan(&promptRecordKey, &promptArtifactKey)
	require.NoError(t, err, "both prompt reference columns should be readable")
	require.Equal(t, "prompt-before", promptRecordKey, "migration should backfill the new prompt reference")
	require.Equal(t, promptRecordKey, promptArtifactKey, "prompt reference columns should stay synchronized")

	var steps, captures string
	err = conn.QueryRow(ctx, `SELECT steps::text, captures::text FROM preview_verification_runs`).Scan(&steps, &captures)
	require.NoError(t, err, "verification history should remain readable")
	require.Contains(t, steps, `"capture"`, "migration should backfill nested verification capture keys")
	require.Contains(t, captures, "capture-before", "migration should backfill the top-level captures collection")
	_, err = conn.Exec(ctx, `UPDATE preview_verification_runs SET steps = '[{"index":2,"artifact":{"id":"capture-late"}}]'`)
	require.NoError(t, err, "draining worker verification updates should remain accepted")
	err = conn.QueryRow(ctx, `SELECT steps::text FROM preview_verification_runs`).Scan(&steps)
	require.NoError(t, err, "late verification steps should remain readable")
	require.Contains(t, steps, `"capture"`, "trigger should normalize nested keys from a draining worker")
	var reviewBundleKey string
	var reviewBundleVersion, reviewBundleFileCount int
	err = conn.QueryRow(ctx, `SELECT review_bundle_key, review_bundle_version, review_bundle_file_count FROM session_diff_snapshots`).Scan(&reviewBundleKey, &reviewBundleVersion, &reviewBundleFileCount)
	require.NoError(t, err, "review bundle metadata should remain readable")
	require.Equal(t, "review-before", reviewBundleKey, "migration should backfill the review bundle key")
	require.Equal(t, 1, reviewBundleVersion, "migration should backfill the review bundle version")
	require.Equal(t, 3, reviewBundleFileCount, "migration should backfill the review bundle file count")

	var cacheKind, metadataKind string
	err = conn.QueryRow(ctx, `SELECT cache_kind, metadata->>'kind' FROM preview_dependency_cache`).Scan(&cacheKind, &metadataKind)
	require.NoError(t, err, "cache metadata should remain readable")
	require.Equal(t, "install_output", cacheKind, "migration should normalize persisted cache kinds")
	require.Equal(t, "install_output", metadataKind, "migration should normalize embedded cache metadata")
	lateCacheID := uuid.New()
	_, err = conn.Exec(ctx, `INSERT INTO preview_dependency_cache (id, cache_kind, metadata) VALUES ($1, 'build_artifact', '{"kind":"build_artifact"}')`, lateCacheID)
	require.NoError(t, err, "draining worker cache writes should remain accepted")
	err = conn.QueryRow(ctx, `SELECT cache_kind, metadata->>'kind' FROM preview_dependency_cache WHERE id = $1`, lateCacheID).Scan(&cacheKind, &metadataKind)
	require.NoError(t, err, "normalized draining-worker cache write should be readable")
	require.Equal(t, "build_output", cacheKind, "trigger should normalize late cache kinds")
	require.Equal(t, "build_output", metadataKind, "trigger should normalize late embedded metadata")
	defaultCacheID := uuid.New()
	_, err = conn.Exec(ctx, `INSERT INTO preview_dependency_cache (id) VALUES ($1)`, defaultCacheID)
	require.NoError(t, err, "cache insert should use the migrated schema default")
	err = conn.QueryRow(ctx, `SELECT cache_kind FROM preview_dependency_cache WHERE id = $1`, defaultCacheID).Scan(&cacheKind)
	require.NoError(t, err, "defaulted cache row should be readable")
	require.Equal(t, "install_output", cacheKind, "installed cache default should use the new value")

	var structuredResult string
	err = conn.QueryRow(ctx, `SELECT structured_result::text FROM code_review_agent_results`).Scan(&structuredResult)
	require.NoError(t, err, "structured review result should remain readable")
	require.Contains(t, structuredResult, "prompt_record_key", "migration should rename persisted prompt result keys")
	require.Contains(t, structuredResult, "raw_record_key", "migration should rename persisted raw result keys")

	downBody, err := os.ReadFile("../../migrations/000280_rename_legacy_output_terms.down.sql")
	require.NoError(t, err, "test should read the output-name down migration")
	_, err = conn.Exec(ctx, string(downBody))
	require.NoError(t, err, "output-name down migration should restore the deployed schema")
	err = conn.QueryRow(ctx, `SELECT count(*) FROM code_review_prompt_artifacts WHERE artifact_key = 'prompt-from-new'`).Scan(&count)
	require.NoError(t, err, "rolled-back prompt table should remain readable")
	require.Equal(t, 1, count, "rollback should preserve writes made through the new table")
	var registration *string
	err = conn.QueryRow(ctx, `SELECT to_regclass('code_review_prompt_records')::text`).Scan(&registration)
	require.NoError(t, err, "rollback table registration should be queryable")
	require.Nil(t, registration, "rollback should remove the expanded prompt table")

	// The compatibility triggers normalize writes toward the new names, so these
	// reverts only take effect if the rollback drops them first.
	err = conn.QueryRow(ctx, `SELECT steps::text FROM preview_verification_runs`).Scan(&steps)
	require.NoError(t, err, "rolled-back verification steps should remain readable")
	require.NotContains(t, steps, `"capture"`, "rollback should strip the nested verification capture keys it added")
	err = conn.QueryRow(ctx, `SELECT cache_kind, metadata->>'kind' FROM preview_dependency_cache WHERE id = $1`, lateCacheID).Scan(&cacheKind, &metadataKind)
	require.NoError(t, err, "rolled-back cache row should remain readable")
	require.Equal(t, "build_artifact", cacheKind, "rollback should restore historical cache kinds")
	require.Equal(t, "build_artifact", metadataKind, "rollback should restore historical embedded cache metadata")
}

const legacyOutputNameTestSchema = `
CREATE TABLE organizations (id uuid PRIMARY KEY);
CREATE TABLE sessions (id uuid PRIMARY KEY, org_id uuid NOT NULL REFERENCES organizations(id));
CREATE TABLE code_review_prompt_artifacts (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    artifact_key text NOT NULL,
    role text NOT NULL CHECK (role IN ('reviewer', 'orchestrator', 'description_policy', 'reviewer_output', 'orchestrator_output')),
    agent_provider text NOT NULL DEFAULT '',
    content text NOT NULL,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_code_review_prompt_artifacts_key ON code_review_prompt_artifacts (org_id, artifact_key);
CREATE INDEX idx_code_review_prompt_artifacts_session ON code_review_prompt_artifacts (org_id, session_id, created_at DESC);
CREATE TABLE code_review_session_metadata (id uuid PRIMARY KEY, prompt_artifact_key text);
CREATE TABLE preview_verification_runs (
    id uuid PRIMARY KEY,
    steps jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(steps) = 'array'),
    artifacts jsonb NOT NULL DEFAULT '[]'::jsonb CONSTRAINT preview_verification_runs_artifacts_check CHECK (jsonb_typeof(artifacts) = 'array')
);
CREATE TABLE session_diff_snapshots (
    id uuid PRIMARY KEY,
    org_id uuid NOT NULL REFERENCES organizations(id),
    session_id uuid NOT NULL REFERENCES sessions(id),
    review_artifact_key text,
    review_artifact_version integer,
    review_artifact_compressed_bytes bigint NOT NULL DEFAULT 0,
    review_artifact_uncompressed_bytes bigint NOT NULL DEFAULT 0,
    review_artifact_file_count integer NOT NULL DEFAULT 0,
    review_artifact_skipped_count integer NOT NULL DEFAULT 0,
    review_artifact_truncated boolean NOT NULL DEFAULT false
);
CREATE INDEX idx_session_diff_snapshots_review_artifact_key
    ON session_diff_snapshots (org_id, session_id, review_artifact_key)
    WHERE review_artifact_key IS NOT NULL;
CREATE TABLE preview_dependency_cache (
    id uuid PRIMARY KEY,
    cache_kind text NOT NULL DEFAULT 'install_artifact',
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE TABLE preview_dependency_cache_locations (
    id uuid PRIMARY KEY,
    cache_kind text NOT NULL DEFAULT 'install_artifact'
);
CREATE TABLE code_review_agent_results (id uuid PRIMARY KEY, structured_result jsonb);`
