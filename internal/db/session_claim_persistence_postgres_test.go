package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// These characterize the v0 persistence extraction only: no lifecycle fence,
// lock ordering, availability decision or execution permission is established.
func TestSessionClaimPersistenceFullSchemaPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, kind, outcome string
		status              models.SessionStatus
	}{
		{name: "idle commit", kind: "idle", outcome: "commit", status: models.SessionStatusIdle},
		{name: "idle rollback", kind: "idle", outcome: "rollback", status: models.SessionStatusIdle},
		{name: "idle foreign organization", kind: "idle", outcome: "foreign", status: models.SessionStatusIdle},
		{name: "idle missing session", kind: "idle", outcome: "missing", status: models.SessionStatusIdle},
		{name: "idle destroyed sandbox", kind: "idle", outcome: "destroyed", status: models.SessionStatusIdle},
		{name: "idle wrong status", kind: "idle", outcome: "ineligible", status: models.SessionStatusRunning},
		{name: "idle preserves legacy deleted visibility", kind: "idle", outcome: "deleted", status: models.SessionStatusIdle},
		{name: "resume completed commit", kind: "resume", outcome: "commit", status: models.SessionStatusCompleted},
		{name: "resume pull request created commit", kind: "resume", outcome: "commit", status: models.SessionStatusPRCreated},
		{name: "resume failed commit", kind: "resume", outcome: "commit", status: models.SessionStatusFailed},
		{name: "resume cancelled commit", kind: "resume", outcome: "commit", status: models.SessionStatusCancelled},
		{name: "resume awaiting input commit", kind: "resume", outcome: "commit", status: models.SessionStatusAwaitingInput},
		{name: "resume guidance commit", kind: "resume", outcome: "commit", status: models.SessionStatusNeedsHumanGuidance},
		{name: "resume rollback", kind: "resume", outcome: "rollback", status: models.SessionStatusCompleted},
		{name: "resume foreign organization", kind: "resume", outcome: "foreign", status: models.SessionStatusCompleted},
		{name: "resume missing session", kind: "resume", outcome: "missing", status: models.SessionStatusCompleted},
		{name: "resume destroyed sandbox", kind: "resume", outcome: "destroyed", status: models.SessionStatusCompleted},
		{name: "resume wrong status", kind: "resume", outcome: "ineligible", status: models.SessionStatusRunning},
		{name: "resume preserves legacy archived visibility", kind: "resume", outcome: "archived", status: models.SessionStatusCompleted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := testpostgres.NewPool(t, context.Background(), "../../migrations", 2)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			f := seedSessionPersistencePostgres(t, ctx, pool)
			_, err := pool.Exec(ctx, `UPDATE sessions SET status=$3,started_at=$4,completed_at=$4,last_activity_at=$4,
			 error='synthetic prior error',failure_explanation='synthetic prior failure',failure_category='synthetic_prior_category',
			 failure_next_steps=ARRAY['synthetic prior next step'],failure_retry_advised=true,
			 runtime_soft_deadline_at=$4,runtime_hard_deadline_at=$4,runtime_last_progress_at=$4,
			 runtime_last_progress_type='tool_use',runtime_last_progress_strength='strong',runtime_extension_count=2,
			 runtime_extension_seconds=17,runtime_stop_reason='no_progress',runtime_graceful_stop_at=$4
			 WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID, tt.status, f.at)
			require.NoError(t, err, "seed all fields whose idle and resume reset contracts intentionally differ")
			switch tt.outcome {
			case "destroyed":
				_, err = pool.Exec(ctx, "UPDATE sessions SET sandbox_state='destroyed' WHERE org_id=$1 AND id=$2", f.orgID, f.sessionID)
			case "deleted":
				_, err = pool.Exec(ctx, "UPDATE sessions SET deleted_at=$3 WHERE org_id=$1 AND id=$2", f.orgID, f.sessionID, f.at)
			case "archived":
				_, err = pool.Exec(ctx, "UPDATE sessions SET archived_at=$3 WHERE org_id=$1 AND id=$2", f.orgID, f.sessionID, f.at)
			}
			require.NoError(t, err, "install only this case's existing legacy eligibility or visibility condition")
			before := claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID)
			owner := beginClaimPersistencePostgresOwner(t, ctx, pool)
			tx := &sessionPersistenceOwnedTx{Tx: owner}
			var transactionID int64
			var at time.Time
			require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current(),now()").Scan(&transactionID, &at), "record the caller's real transaction identity and legacy timestamp")
			_, err = owner.Exec(ctx, "UPDATE sessions SET title='Synthetic outer claim unit' WHERE org_id=$1 AND id=$2", f.orgID, f.sessionID)
			require.NoError(t, err, "stage a genuine earlier owner write so whole-unit rollback cannot pass vacuously")
			stagedBefore := claimPersistencePostgresRows(t, ctx, owner, f.orgID, f.sessionID)
			baseline := claimPersistencePostgresSession(t, ctx, owner, f.orgID, f.sessionID)
			store, redis := sessionPersistenceStoreWithStreams(t, nil)
			requested := f
			if tt.outcome == "foreign" {
				requested.orgID = uuid.New()
			} else if tt.outcome == "missing" {
				requested.sessionID = uuid.New()
			}
			actual, claimErr := callSessionClaimPersistence(store, ctx, tx, requested, tt.kind, "explicit")
			require.Empty(t, tx.settlements, "the claim must not begin, settle or retry its caller's unit")
			require.Empty(t, redis.Keys(), "enabled Redis publishers must remain silent while the owner's row changes are uncommitted")
			require.Equal(t, before, claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID), "a second pool reader must see every original parent, lane and persistence-side row before settlement")
			requireClaimPersistencePostgresOwner(t, ctx, owner, transactionID)
			if tt.outcome == "foreign" || tt.outcome == "missing" || tt.outcome == "destroyed" || tt.outcome == "ineligible" {
				require.Equal(t, pgx.ErrNoRows, claimErr, "legacy scope and status rejection must retain its exact missing-row sentinel")
				require.Equal(t, models.Session{}, actual, "rejected parent claims must return no accepted row")
				require.Equal(t, stagedBefore, claimPersistencePostgresRows(t, ctx, owner, f.orgID, f.sessionID), "claim rejection must leave the caller's earlier staged unit completely unchanged")
				require.NoError(t, owner.Rollback(ctx), "only the owner may roll back the earlier write and rejected claim together")
				require.Equal(t, before, claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID), "rollback must restore every before-snapshot column after claim rejection")
				require.Empty(t, redis.Keys(), "a rejected and rolled-back parent claim must publish no enabled-Redis event")
				return
			}
			require.NoError(t, claimErr, "parent claim persistence must use the supplied transaction even though the store has no database handle")
			expected := expectedSessionClaimPersistencePostgres(baseline, tt.kind, at)
			require.Equal(t, expected, actual, "idle and resume claims must preserve the complete exact legacy returned row, including their different failure/runtime resets")
			require.Equal(t, expected, claimPersistencePostgresSession(t, ctx, owner, f.orgID, f.sessionID), "owner-visible stored projection must equal the exact claimed row")
			staged := claimPersistencePostgresRows(t, ctx, owner, f.orgID, f.sessionID)
			fields := []string{"status", "started_at", "last_activity_at", "completed_at", "runtime_soft_deadline_at", "runtime_hard_deadline_at"}
			if tt.kind == "idle" {
				fields = append(fields, "error", "failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised")
			} else {
				fields = append(fields, "runtime_last_progress_at", "runtime_last_progress_type", "runtime_last_progress_strength", "runtime_extension_count", "runtime_extension_seconds", "runtime_stop_reason", "runtime_graceful_stop_at")
			}
			requireClaimPersistencePostgresDelta(t, stagedBefore, staged, uuid.Nil, fields)
			require.NotEqual(t, stagedBefore, staged, "commit and rollback proof must follow a genuine claim mutation")
			if tt.outcome == "rollback" {
				require.NoError(t, owner.Rollback(ctx), "the owner may reject a successful parent claim as part of a larger unit")
				require.Equal(t, before, claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID), "rollback must restore the complete original snapshot including the earlier owner write")
				require.Empty(t, redis.Keys(), "discarded successful claims must publish no event")
				return
			}
			require.NoError(t, owner.Commit(ctx), "only the outer owner may commit parent claim persistence")
			require.Equal(t, staged, claimPersistencePostgresRows(t, ctx, pool, f.orgID, f.sessionID), "commit must persist exactly the staged legacy row changes and no hidden independent write")
			require.Empty(t, redis.Keys(), "the legacy parent claim itself has no notification, including after commit")
			// This separate existing adapter is an explicit owner action; the
			// extraction does not add notifications to the legacy claim wrapper.
			store.PublishCommittedSessionStatus(ctx, f.orgID, actual)
			entries, err := redis.Stream("143:stream:{ses:" + f.sessionID.String() + "}:status")
			require.NoError(t, err, "the owner may explicitly publish its confirmed committed row through the existing status adapter")
			var events []string
			for _, entry := range entries {
				events = append(events, entry.Values[1])
			}
			encoded, err := json.Marshal(expected)
			require.NoError(t, err, "encode the full exact claimed row without a post-commit database reload")
			require.Equal(t, []string{string(encoded)}, events, "explicit owner publication must emit only the complete captured legacy row")
		})
	}
}

func expectedSessionClaimPersistencePostgres(before models.Session, kind string, at time.Time) models.Session {
	expected := before
	expected.Status, expected.StartedAt, expected.LastActivityAt, expected.CompletedAt = models.SessionStatusRunning, &at, at, nil
	expected.RuntimeSoftDeadlineAt, expected.RuntimeHardDeadlineAt = nil, nil
	if kind == "idle" {
		expected.Error, expected.FailureExplanation, expected.FailureCategory, expected.FailureNextSteps = nil, nil, nil, nil
		expected.FailureRetryAdvised = false
	} else {
		expected.RuntimeLastProgressAt, expected.RuntimeGracefulStopAt = nil, nil
		expected.RuntimeLastProgressType, expected.RuntimeLastProgressStrength, expected.RuntimeStopReason = "", "", ""
		expected.RuntimeExtensionCount, expected.RuntimeExtensionSeconds = 0, 0
	}
	return expected
}

func beginClaimPersistencePostgresOwner(t *testing.T, ctx context.Context, fixturePool *pgxpool.Pool) pgx.Tx {
	t.Helper()
	configuration := fixturePool.Config()
	configuration.MaxConns, configuration.MinConns = 1, 0
	pool, err := pgxpool.NewWithConfig(ctx, configuration)
	require.True(t, err == nil, "open an owner-only pool against this isolated fixture without printing connection details")
	t.Cleanup(pool.Close)
	owner, err := pool.Begin(ctx)
	require.NoError(t, err, "only the caller may begin and occupy its sole owner-pool connection")
	t.Cleanup(func() { _ = owner.Rollback(context.Background()) })
	return owner
}

func requireClaimPersistencePostgresOwner(t *testing.T, ctx context.Context, owner pgx.Tx, expected int64) {
	t.Helper()
	var actual int64
	require.NoError(t, owner.QueryRow(ctx, "SELECT txid_current()").Scan(&actual), "the caller's transaction must remain usable after each persistence result")
	require.Equal(t, expected, actual, "the persistence extraction must not replace or settle the original PostgreSQL transaction")
}

func claimPersistencePostgresSession(t *testing.T, ctx context.Context, database DBTX, orgID, sessionID uuid.UUID) models.Session {
	t.Helper()
	rows, err := database.Query(ctx, "SELECT "+sessionSelectColumns+" FROM sessions WHERE org_id=$1 AND id=$2", orgID, sessionID)
	require.NoError(t, err, "read the complete returned-row projection without narrowing the legacy deleted or archived visibility")
	session, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Session])
	require.NoError(t, err, "collect every projected parent field before comparing exact v0 mutations")
	hydrateSessionPolicy(&session)
	return session
}

func claimPersistencePostgresRows(t *testing.T, ctx context.Context, database DBTX, orgID, sessionID uuid.UUID) json.RawMessage {
	t.Helper()
	var rows json.RawMessage
	err := database.QueryRow(ctx, `SELECT jsonb_build_object(
	 'organization',(SELECT to_jsonb(o) FROM organizations o WHERE o.id=$1),
	 'session',(SELECT to_jsonb(s) FROM sessions s WHERE s.org_id=$1 AND s.id=$2),
	 'threads',(SELECT jsonb_agg(to_jsonb(t) ORDER BY t.id) FROM session_threads t WHERE t.org_id=$1 AND t.session_id=$2),
	 'phases',(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.id) FROM session_activity_phases p WHERE p.org_id=$1 AND p.session_id=$2),
	 'publish',(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.session_id) FROM session_publish_state p WHERE p.org_id=$1 AND p.session_id=$2),
	 'changesets',(SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM session_changesets c WHERE c.org_id=$1 AND c.session_id=$2),
	 'snapshots',(SELECT jsonb_agg(to_jsonb(d) ORDER BY d.id) FROM session_diff_snapshots d WHERE d.org_id=$1 AND d.session_id=$2))`, orgID, sessionID).Scan(&rows)
	require.NoError(t, err, "snapshot all raw parent, sibling and persistence-side columns without visibility filtering or lossy numeric conversion")
	return rows
}

// Compare every raw column outside the mutation's explicit v0 field set. The
// full model comparison separately checks each allowed field's exact value.
func requireClaimPersistencePostgresDelta(t *testing.T, before, after json.RawMessage, threadID uuid.UUID, fields []string) {
	t.Helper()
	graphs := make([]map[string]json.RawMessage, 2)
	for i, raw := range []json.RawMessage{before, after} {
		require.NoError(t, json.Unmarshal(raw, &graphs[i]), "decode the complete raw graph while preserving every individual value")
		if threadID == uuid.Nil {
			var row map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(graphs[i]["session"], &row), "decode the exact parent row before masking only legacy claim fields")
			for _, field := range fields {
				delete(row, field)
			}
			encoded, err := json.Marshal(row)
			require.NoError(t, err, "retain all unmodified raw parent fields for exact comparison")
			graphs[i]["session"] = encoded
		} else {
			var rows []map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(graphs[i]["threads"], &rows), "decode every raw sibling row before isolating the explicit target")
			found := false
			for _, row := range rows {
				var id uuid.UUID
				require.NoError(t, json.Unmarshal(row["id"], &id), "identify the explicit target without omitting any sibling")
				if id == threadID {
					found = true
					for _, field := range fields {
						delete(row, field)
					}
				}
			}
			require.True(t, found, "raw graph comparison must locate the exact existing target rather than pass vacuously")
			encoded, err := json.Marshal(rows)
			require.NoError(t, err, "retain all sibling rows and all other target fields for exact comparison")
			graphs[i]["threads"] = encoded
		}
	}
	require.Equal(t, graphs[0], graphs[1], "claim persistence must change only its exact legacy fields, not parent policy, siblings, passive claims, phases, publication, changesets or snapshots")
}
