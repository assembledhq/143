package db

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestFinishPublicationReviewPostgresQualifiesCompletedAtAcrossJoinedTables(t *testing.T) {
	t.Parallel()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL to run PostgreSQL publication review transition tests")
	}
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, databaseURL)
	require.NoError(t, err, "test should connect to TEST_DATABASE_URL")
	schema := "test_publication_review_" + strings.ReplaceAll(uuid.NewString(), "-", "_")
	_, err = adminPool.Exec(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err, "test should create an isolated publication review schema")

	config, err := pgxpool.ParseConfig(databaseURL)
	require.NoError(t, err, "test should parse the isolated pool configuration")
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err, "test should connect with the isolated search path")
	t.Cleanup(func() {
		pool.Close()
		_, cleanupErr := adminPool.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		require.NoError(t, cleanupErr, "test should remove the isolated publication review schema")
		adminPool.Close()
	})

	_, err = pool.Exec(ctx, `
		CREATE TABLE sessions (
			id uuid PRIMARY KEY,
			org_id uuid NOT NULL,
			workspace_revision bigint NOT NULL,
			completed_at timestamptz
		);
		CREATE TABLE session_changesets (
			id uuid PRIMARY KEY,
			org_id uuid NOT NULL,
			session_id uuid NOT NULL,
			head_sha text,
			completed_at timestamptz
		);
		CREATE TABLE session_review_loops (
			id uuid PRIMARY KEY,
			org_id uuid NOT NULL,
			session_id uuid NOT NULL,
			workspace_revision bigint,
			desired_head_sha text,
			completed_at timestamptz
		);
		CREATE TABLE session_publications (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			org_id uuid NOT NULL,
			session_id uuid NOT NULL,
			changeset_id uuid NOT NULL,
			review_loop_id uuid,
			state text NOT NULL,
			review_gate_state text NOT NULL,
			review_workspace_revision bigint,
			review_desired_head_sha text,
			request_payload jsonb NOT NULL DEFAULT '{}'::jsonb,
			job_queue text NOT NULL DEFAULT 'agent',
			completed_at timestamptz,
			updated_at timestamptz NOT NULL DEFAULT now()
		)`)
	require.NoError(t, err, "test should create the minimal publication review schema")

	orgID, sessionID, changesetID, loopID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	headSHA := "0123456789abcdef0123456789abcdef01234567"
	_, err = pool.Exec(ctx, `INSERT INTO sessions (id, org_id, workspace_revision) VALUES ($1, $2, 7)`, sessionID, orgID)
	require.NoError(t, err, "test should seed the reviewed session")
	_, err = pool.Exec(ctx, `INSERT INTO session_changesets (id, org_id, session_id, head_sha) VALUES ($1, $2, $3, $4)`, changesetID, orgID, sessionID, headSHA)
	require.NoError(t, err, "test should seed the reviewed changeset")
	_, err = pool.Exec(ctx, `INSERT INTO session_review_loops (id, org_id, session_id, workspace_revision, desired_head_sha) VALUES ($1, $2, $3, 7, $4)`, loopID, orgID, sessionID, headSHA)
	require.NoError(t, err, "test should seed the publication review loop")
	_, err = pool.Exec(ctx, `
		INSERT INTO session_publications (
			org_id, session_id, changeset_id, review_loop_id, state, review_gate_state,
			review_workspace_revision, review_desired_head_sha
		) VALUES ($1, $2, $3, $4, 'review_pending', 'pending', 7, $5)`,
		orgID, sessionID, changesetID, loopID, headSHA)
	require.NoError(t, err, "test should seed a linked publication review")

	err = finishPublicationReviewOn(ctx, pool, orgID, loopID, models.ReviewLoopStatusFailed)
	require.NoError(t, err, "terminal publication review transition should execute against PostgreSQL without an ambiguous completed_at reference")

	var state models.SessionPublicationState
	var gate models.SessionPublicationReviewGateState
	var completed bool
	err = pool.QueryRow(ctx, `
		SELECT state, review_gate_state, completed_at IS NOT NULL
		FROM session_publications
		WHERE org_id = $1 AND review_loop_id = $2`, orgID, loopID).Scan(&state, &gate, &completed)
	require.NoError(t, err, "test should reload the terminal publication state")
	require.Equal(t, models.SessionPublicationStateTerminalFailed, state, "failed review should terminally fail publication")
	require.Equal(t, models.SessionPublicationReviewGateFailed, gate, "failed review should fail the review gate")
	require.True(t, completed, "terminal publication should record completed_at")
}
