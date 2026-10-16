package main

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/publicationintent"
	"github.com/assembledhq/143/internal/worker"
)

func newPublicationWiringPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL for publication wiring tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, raw)
	require.NoError(t, err, "connect to disposable PostgreSQL server")
	t.Cleanup(func() { require.NoError(t, admin.Close(ctx), "close publication test admin connection") })
	name := "publication_wiring_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = admin.Exec(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err, "create isolated publication wiring database")
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		require.NoError(t, err, "remove isolated publication wiring database")
	})
	parsed, err := url.Parse(raw)
	require.NoError(t, err, "parse disposable database URL")
	parsed.Path = "/" + name
	source, err := filepath.Abs("../../migrations")
	require.NoError(t, err, "resolve complete schema migration directory")
	migration, err := migrate.New("file://"+source, parsed.String())
	require.NoError(t, err, "initialize isolated publication migrations")
	require.NoError(t, migration.Up(), "publication wiring must work with the complete current schema")
	sourceErr, dbErr := migration.Close()
	require.NoError(t, sourceErr, "close publication migration source")
	require.NoError(t, dbErr, "close publication migration database")
	pool, err := pgxpool.New(ctx, parsed.String())
	require.NoError(t, err, "open publication wiring database pool")
	t.Cleanup(pool.Close)
	return pool
}

func TestSessionExecutorPublicationWiringQueuesAutomationPRPostgres(t *testing.T) {
	t.Parallel()
	pool := newPublicationWiringPool(t)
	tests := []struct {
		name     string
		mode     models.PRHandoffMode
		githubID int64
	}{
		{"review before publication", models.PRHandoffModePrePublish, 1},
		{"draft first", models.PRHandoffModeDraftFirst, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			stores := buildSessionExecutorStores(sessionExecutorStoreDeps{Pool: pool})
			services := &worker.Services{}
			wirePublicationCoordinator(&config.Config{AgentPRPublicationEnabled: false, PrePRReviewEnabled: true}, services, stores, zerolog.Nop())
			require.NotNil(t, services.PublicationIntents, "executor must have a coordinator before completing automation work")

			orgID, integrationID, repoID, automationID, runID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Publication fixture')`, orgID)
			require.NoError(t, err, "create isolated publication tenant")
			_, err = pool.Exec(ctx, `INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, integrationID, orgID)
			require.NoError(t, err, "create fixture GitHub integration")
			settings, err := json.Marshal(models.RepositorySettings{PRHandoffMode: tt.mode})
			require.NoError(t, err, "encode repository handoff policy")
			_, err = pool.Exec(ctx, `INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id,settings)
				VALUES($1,$2,$3,$4,'example/fixture','https://example.test/fixture.git',1,$5)`, repoID, orgID, integrationID, tt.githubID, settings)
			require.NoError(t, err, "create repository with explicit handoff policy")
			_, err = pool.Exec(ctx, `INSERT INTO automations(id,org_id,repository_id,name,goal) VALUES($1,$2,$3,'Publication fixture','Update fixture')`, automationID, orgID, repoID)
			require.NoError(t, err, "create publication-enabled automation fixture")
			_, err = pool.Exec(ctx, `INSERT INTO automation_runs(id,automation_id,org_id,goal_snapshot,status,config_snapshot)
				VALUES($1,$2,$3,'Update fixture','running','{"publish_policy":"pull_request","pre_pr_review_loops":0}')`, runID, automationID, orgID)
			require.NoError(t, err, "freeze automatic publication policy on the run")
			baseBranch, headBranch := "main", "codex/publication-fixture"
			session := &models.Session{
				OrgID: orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusCompleted,
				AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.DefaultSessionTokenMode,
				Origin: models.SessionOriginAutomation, InteractionMode: models.SessionInteractionModeSingleRun,
				ValidationPolicy: models.SessionValidationPolicyOnTurnComplete, RepositoryID: &repoID,
				AutomationRunID: &runID, TargetBranch: &baseBranch,
			}
			require.NoError(t, stores.Sessions.Create(ctx, session), "create completed automation session and primary changeset")
			require.NoError(t, stores.Sessions.UpdateWorkingBranch(ctx, orgID, session.ID, headBranch), "record the automation working branch")
			changeset, err := stores.SessionChangesets.GetPrimary(ctx, orgID, session.ID)
			require.NoError(t, err, "resolve publication changeset created by the session lifecycle")
			request := publicationintent.RequestPullRequest{Source: models.SessionPublicationSourceAutomation, TriggerKind: models.SessionPublicationTriggerPolicy}
			result, err := services.PublicationIntents.RequestPullRequest(ctx, orgID, session.ID, request)
			require.NoError(t, err, "automation policy must publish with the agent-tool publication switch disabled")
			require.Equal(t, publicationintent.ResultPRQueued, result.Status, "executor coordinator must queue PR creation")
			require.NotNil(t, result.PublicationID, "queued request must expose its durable publication identity")
			repeated, err := services.PublicationIntents.RequestPullRequest(ctx, orgID, session.ID, request)
			require.NoError(t, err, "repeated automation completion must reuse the durable request")
			require.Equal(t, result.PublicationID, repeated.PublicationID, "repeated completion must preserve publication identity")

			publication, err := stores.SessionPublications.GetByChangeset(ctx, orgID, session.ID, changeset.ID)
			require.NoError(t, err, "publication request must survive in PostgreSQL")
			expectedPayload := map[string]any{
				"session_id": session.ID.String(), "changeset_id": changeset.ID.String(), "org_id": orgID.String(),
				"publication_source": "automation", "publication_queue": "default", "publication_trigger_kind": "policy",
				"publication_handoff_mode": string(tt.mode), "automatic_pr_policy_source": "automation", "review_policy_source": "automation",
				"initiated_by_user_id": nil,
			}
			if tt.mode == models.PRHandoffModeDraftFirst {
				expectedPayload["draft"] = true
			}
			var durablePayload map[string]any
			require.NoError(t, json.Unmarshal(publication.RequestPayload, &durablePayload), "decode durable publication options")
			require.Equal(t, expectedPayload, durablePayload, "durable intent must preserve automatic authority and repository handoff policy")
			require.Equal(t, models.SessionPublicationSourceAutomation, publication.Source, "publication must retain automation ownership")
			require.Equal(t, models.SessionPublicationTriggerPolicy, publication.TriggerKind, "publication must retain the policy trigger")
			require.Equal(t, tt.mode, publication.HandoffMode, "executor must load repository handoff settings")

			type queuedJob struct {
				Queue, Type, Status, DedupeKey string
				Priority                       int
				Payload                        map[string]any
			}
			rows, err := pool.Query(ctx, `SELECT queue AS "Queue",job_type AS "Type",status AS "Status",dedupe_key AS "DedupeKey",priority AS "Priority",payload AS "Payload"
				FROM jobs WHERE org_id=$1 ORDER BY created_at,id`, orgID)
			require.NoError(t, err, "read all jobs for the automation tenant")
			jobs, err := pgx.CollectRows(rows, pgx.RowToStructByName[queuedJob])
			require.NoError(t, err, "decode queued publication jobs")
			require.Equal(t, []queuedJob{{Queue: "default", Type: "open_pr", Status: "pending", DedupeKey: db.OpenPRDedupeKey(changeset.ID), Priority: 0, Payload: expectedPayload}}, jobs, "completion and replay must produce exactly one default-worker PR job")
		})
	}
}
