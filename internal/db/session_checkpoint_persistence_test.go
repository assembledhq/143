package db

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type checkpointPersistenceFixture struct {
	orgID, sessionID, token uuid.UUID
	at                      time.Time
	kind                    models.CheckpointKind
	provenance              models.CheckpointProvenance
}

func newCheckpointPersistenceFixture() checkpointPersistenceFixture {
	fingerprint := "synthetic-dependencies"
	return checkpointPersistenceFixture{
		orgID: uuid.New(), sessionID: uuid.New(), token: uuid.New(),
		at:         time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.FixedZone("synthetic", 3600)),
		kind:       models.CheckpointKindTurnComplete,
		provenance: models.CheckpointProvenance{GenerationID: uuid.New(), HeadSHA: " synthetic-head ", DependencyFingerprint: &fingerprint, ReviewComplete: true},
	}
}

func TestCheckpointPersistenceStandaloneAndInTx(t *testing.T) {
	t.Parallel()
	tests := []struct{ mode, variant string }{}
	for _, mode := range []string{"standalone", "transaction"} {
		for _, variant := range []string{"success", "no token", "bootstrap", "graceful", "nil fingerprint", "empty head", "no row", "query error", "scan error", "collection error"} {
			tests = append(tests, struct{ mode, variant string }{mode, variant})
		}
	}
	for _, tt := range tests {
		t.Run(tt.mode+"/"+tt.variant, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newCheckpointPersistenceFixture()
			switch tt.variant {
			case "no token":
				f.token = uuid.Nil
			case "bootstrap":
				f.kind = models.CheckpointKindBootstrap
			case "graceful":
				f.kind = models.CheckpointKindGracefulStop
			case "nil fingerprint":
				f.provenance.DependencyFingerprint = nil
			case "empty head":
				f.provenance.HeadSHA = " \t "
			}
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create an isolated exact-statement checkpoint mock")
			t.Cleanup(mock.Close)
			store, redis := sessionPersistenceStoreWithStreams(t, checkpointDBTXOnly{mock})
			var owner pgx.Tx
			var tx *sessionPersistenceOwnedTx
			if tt.mode == "transaction" {
				mock.ExpectBegin()
				owner, err = mock.Begin(ctx)
				require.NoError(t, err, "the caller alone starts the concrete checkpoint transaction")
				tx = &sessionPersistenceOwnedTx{Tx: owner}
				store.db = nil
			}
			failure := errors.New("synthetic checkpoint write failure")
			write := mock.ExpectQuery(regexp.QuoteMeta(checkpointPersistenceQuery(f.token))).WithArgs(checkpointPersistenceArgs(f))
			switch tt.variant {
			case "query error":
				write.WillReturnError(failure)
			case "no row":
				write.WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}))
			case "scan error":
				write.WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).AddRow("not a revision", f.at))
			case "collection error":
				write.WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).AddRow(int64(9), f.at).RowError(0, failure))
			default:
				write.WillReturnRows(pgxmock.NewRows([]string{"workspace_revision", "workspace_revision_updated_at"}).AddRow(int64(9), f.at))
			}
			var published bool
			var effects SessionPersistenceEffects
			if tx == nil {
				published, err = store.PublishCheckpointWithProvenance(ctx, f.orgID, f.sessionID, f.token, "synthetic-agent", " synthetic-checkpoint ", f.kind, models.CheckpointCapabilityFullResume, 1234, f.at, models.RuntimeStopReasonSoftBudget, f.provenance)
			} else {
				published, effects, err = store.PublishCheckpointWithProvenanceInTx(ctx, f.orgID, tx, f.sessionID, f.token, "synthetic-agent", " synthetic-checkpoint ", f.kind, models.CheckpointCapabilityFullResume, 1234, f.at, models.RuntimeStopReasonSoftBudget, f.provenance)
				require.Empty(t, redis.Keys(), "the concrete seam must never publish through enabled parent streams")
				require.Empty(t, tx.settlements, "the concrete seam must not begin, settle, retry or savepoint")
			}
			expectedError := ""
			switch tt.variant {
			case "query error":
				expectedError = "publish checkpoint with provenance: " + failure.Error()
			case "collection error":
				expectedError = "collect published checkpoint with provenance: " + failure.Error()
			}
			failed := tt.variant == "query error" || tt.variant == "scan error" || tt.variant == "collection error"
			if failed {
				require.Error(t, err, "query and collection failures must remain rejected with no captured event")
				if expectedError != "" {
					require.EqualError(t, err, expectedError, "preserve the existing exact checkpoint error prefixes and causes")
					require.ErrorIs(t, err, failure, "checkpoint errors must retain their original cause")
				} else {
					require.ErrorContains(t, err, "collect published checkpoint with provenance:", "scan failures must retain the original collection prefix")
				}
			} else {
				require.NoError(t, err, "success and a rejected row preserve the existing error-free return")
			}
			accepted := !failed && tt.variant != "no row"
			require.Equal(t, accepted, published, "only a successfully collected joined checkpoint row is accepted")
			if tx != nil {
				expectedEffects := SessionPersistenceEffects{}
				if accepted {
					expectedEffects = SessionPersistenceEffects{orgID: f.orgID, sessionID: f.sessionID, workspaceRevision: workspaceRevisionUpdate{Revision: 9, UpdatedAt: f.at}, workspaceChanged: true, workspaceReason: "checkpoint"}
				}
				require.Equal(t, expectedEffects, effects, "capture only opaque exact returned revision, timestamp, tenant and checkpoint reason")
				if accepted {
					mock.ExpectCommit()
					require.NoError(t, owner.Commit(ctx), "only the caller may physically commit its checkpoint unit")
					require.Empty(t, redis.Keys(), "the commit itself must not implicitly publish captured effects")
					store.PublishCommittedSessionPersistenceEffects(ctx, f.orgID, effects)
				} else {
					mock.ExpectRollback()
					require.NoError(t, owner.Rollback(ctx), "the caller must roll back both false and failed persistence")
				}
			}
			requireCheckpointPersistenceEvents(t, redis, f.orgID, f.sessionID, f.at.UTC(), accepted, "checkpoint")
			require.NoError(t, mock.ExpectationsWereMet(), "standalone remains a single DBTX statement and the seam uses only its supplied transaction")
		})
	}
}

type checkpointDBTXOnly struct{ DBTX }

func TestCheckpointPersistenceRejectsInvalidArguments(t *testing.T) {
	t.Parallel()
	var typedNil *sessionPersistenceOwnedTx
	tests := []struct {
		name, mode, key, expected string
		generation                uuid.UUID
		tx                        pgx.Tx
	}{
		{name: "nil transaction", mode: "transaction", key: "key", generation: uuid.New(), expected: "persist checkpoint with provenance: transaction is required"},
		{name: "typed nil transaction", mode: "transaction", key: "key", generation: uuid.New(), tx: typedNil, expected: "persist checkpoint with provenance: transaction is required"},
		{name: "standalone empty key", key: " \t ", generation: uuid.New(), expected: "publish checkpoint with provenance: snapshot key is required"},
		{name: "standalone missing generation", key: "key", expected: "publish checkpoint with provenance: generation is required"},
		{name: "transaction empty key", mode: "transaction", key: " \t ", generation: uuid.New(), expected: "publish checkpoint with provenance: snapshot key is required"},
		{name: "transaction missing generation", mode: "transaction", key: "key", expected: "publish checkpoint with provenance: generation is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store, redis := sessionPersistenceStoreWithStreams(t, nil)
			var published bool
			var effects SessionPersistenceEffects
			var err error
			f := newCheckpointPersistenceFixture()
			f.provenance.GenerationID = tt.generation
			if tt.mode == "transaction" {
				tx := tt.tx
				if tt.name != "nil transaction" && tt.name != "typed nil transaction" {
					tx = &sessionPersistenceOwnedTx{}
				}
				published, effects, err = store.PublishCheckpointWithProvenanceInTx(context.Background(), f.orgID, tx, f.sessionID, f.token, "", tt.key, f.kind, models.CheckpointCapabilityFullResume, 1, f.at, models.RuntimeStopReasonNone, f.provenance)
			} else {
				published, err = store.PublishCheckpointWithProvenance(context.Background(), f.orgID, f.sessionID, f.token, "", tt.key, f.kind, models.CheckpointCapabilityFullResume, 1, f.at, models.RuntimeStopReasonNone, f.provenance)
			}
			require.EqualError(t, err, tt.expected, "reject invalid arguments before any database or publication dependency is accessed")
			require.False(t, published, "invalid inputs cannot report an accepted checkpoint")
			require.Equal(t, SessionPersistenceEffects{}, effects, "invalid inputs must not fabricate captured effects")
			require.Empty(t, redis.Keys(), "invalid inputs must not emit checkpoint or status events")
		})
	}
}

func TestCheckpointPersistenceEffectReasonAndTenant(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, reason, expectedReason        string
		foreign, empty, notificationFailure bool
	}{
		{name: "legacy effects retain diff reason", expectedReason: "diff_snapshot"},
		{name: "checkpoint effects retain checkpoint reason", reason: "checkpoint", expectedReason: "checkpoint"},
		{name: "foreign effects are refused", reason: "checkpoint", foreign: true},
		{name: "zero effects are silent", empty: true},
		{name: "optional Redis failure remains best effort", reason: "checkpoint", notificationFailure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newCheckpointPersistenceFixture()
			store, redis := sessionPersistenceStoreWithStreams(t, nil)
			effects := SessionPersistenceEffects{orgID: f.orgID, sessionID: f.sessionID, workspaceRevision: workspaceRevisionUpdate{Revision: 9, UpdatedAt: f.at}, workspaceChanged: true, workspaceReason: tt.reason}
			if tt.empty {
				effects = SessionPersistenceEffects{}
			}
			orgID := f.orgID
			if tt.foreign {
				orgID = uuid.New()
			}
			if tt.notificationFailure {
				redis.SetError("ERR synthetic checkpoint notification failure")
			}
			store.PublishCommittedSessionPersistenceEffects(context.Background(), orgID, effects)
			requireCheckpointPersistenceEvents(t, redis, f.orgID, f.sessionID, f.at.UTC(), !tt.foreign && !tt.empty && !tt.notificationFailure, tt.expectedReason)
		})
	}
}

func checkpointPersistenceArgs(f checkpointPersistenceFixture) pgx.NamedArgs {
	args := pgx.NamedArgs{"id": f.sessionID, "org_id": f.orgID, "generation_id": f.provenance.GenerationID, "agent_session_id": "synthetic-agent", "snapshot_key": " synthetic-checkpoint ", "checkpoint_kind": string(f.kind), "checkpoint_capability": string(models.CheckpointCapabilityFullResume), "checkpoint_size_bytes": int64(1234), "checkpointed_at": f.at.UTC(), "checkpoint_error": nil, "runtime_stop_reason": string(models.RuntimeStopReasonSoftBudget), "checkpoint_head_sha": nullIfEmpty(f.provenance.HeadSHA), "dependency_fingerprint": f.provenance.DependencyFingerprint, "review_complete": f.provenance.ReviewComplete && f.kind == models.CheckpointKindTurnComplete}
	if f.token != uuid.Nil {
		args["lock_token"] = f.token
	}
	return args
}

func checkpointPersistenceQuery(token uuid.UUID) string {
	query := `WITH published AS (` + publishCheckpointUpdate + `
		  AND s.automation_owner_generation_id = @generation_id`
	if token != uuid.Nil {
		query += publishCheckpointJobFence
	}
	return query + `
			RETURNING s.id, s.org_id, s.workspace_revision, s.workspace_revision_updated_at
		), provenance AS (
			UPDATE automation_target_sessions g
			SET checkpoint_snapshot_key = @snapshot_key,
			    checkpoint_head_sha = @checkpoint_head_sha,
			    checkpoint_dependency_fingerprint = @dependency_fingerprint,
			    checkpoint_review_complete = @review_complete,
			    updated_at = now()
			FROM published p
			WHERE g.id = @generation_id AND g.org_id = p.org_id AND g.session_id = p.id
			RETURNING g.id
		)
		SELECT p.workspace_revision, p.workspace_revision_updated_at
		FROM published p
		JOIN provenance g ON true`
}

func requireCheckpointPersistenceEvents(t *testing.T, redis *miniredis.Miniredis, orgID, sessionID uuid.UUID, at time.Time, accepted bool, reason string) {
	t.Helper()
	if !accepted {
		require.Empty(t, redis.Keys(), "rejected persistence and refused or failed optional effects leave all streams empty")
		return
	}
	key := "143:stream:{ses:" + sessionID.String() + "}:events"
	require.Equal(t, []string{key}, redis.Keys(), "checkpoint effects publish only the existing event stream, never status")
	entries, err := redis.Stream(key)
	require.NoError(t, err, "read the exact explicitly published checkpoint wake")
	var actual []models.SessionStreamEvent
	for _, entry := range entries {
		var event models.SessionStreamEvent
		require.NoError(t, json.Unmarshal([]byte(entry.Values[1]), &event), "decode every field of the original workspace event")
		actual = append(actual, event)
	}
	data, err := json.Marshal(models.SessionWorkspaceGenerationChangedEvent{SessionID: sessionID, OrgID: orgID, WorkspaceRevision: 9, WorkspaceRevisionUpdatedAt: at, Reason: reason})
	require.NoError(t, err, "encode the exact expected tenant, revision, timestamp and reason")
	var expectedData any
	require.NoError(t, json.Unmarshal(data, &expectedData), "normalize only JSON interface data for full event equality")
	require.Equal(t, []models.SessionStreamEvent{{Type: models.SessionStreamEventWorkspaceGenerationChanged, SessionID: sessionID, OrgID: orgID, Data: expectedData}}, actual, "retain the complete original workspace notification without extra status, authority or timing fields")
}
