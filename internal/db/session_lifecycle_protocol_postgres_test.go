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

// These full-schema tests establish retained protocol provenance and transaction
// mechanics only. Synthetic fixture receipts neither activate a real cohort nor
// establish provider conformance, minimum-writer enforcement or child guards.
func TestLifecycleProtocolModeMatrixPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	fixture := seedActivatedLifecycleFixture(t, context.Background(), pool)
	tests := []struct {
		name                                                           string
		stamp                                                          int16
		cutover                                                        string
		revision                                                       int64
		mutate                                                         func(*testing.T, *lifecycleCutoverObservation)
		want                                                           lifecycleModeObservation
		provenance                                                     bool
		laneZero, laneOtherProvider, parentOtherProvider, positiveLane bool
	}{
		{name: "never activated parent without cutover", want: lifecycleModeObservation{mode: lifecycleModeLegacy}},
		{name: "inert shadow revision zero", cutover: "shadow", want: lifecycleModeObservation{mode: lifecycleModeLegacy}},
		{name: "inert shadow revision seventeen", cutover: "shadow", revision: 17, want: lifecycleModeObservation{mode: lifecycleModeLegacy, cutoverVersion: 17}},
		{name: "normalization pending is not legacy", cutover: "normalizing", revision: 17, want: lifecycleModeObservation{mode: lifecycleModeNormalizing, cutoverVersion: 17}},
		{name: "active protocol one with CAS revision zero", stamp: 1, cutover: "active", want: lifecycleModeObservation{mode: lifecycleModeActive, protocolVersion: 1}},
		{name: "active protocol one with CAS revision seventeen", stamp: 1, cutover: "active", revision: 17, want: lifecycleModeObservation{mode: lifecycleModeActive, protocolVersion: 1, cutoverVersion: 17}},
		{name: "active visible lane zero stamp fails closed", stamp: 1, cutover: "active", laneZero: true, provenance: true},
		{name: "active visible lane provider must be covered", stamp: 1, cutover: "active", laneOtherProvider: true, provenance: true},
		{name: "active parent provider must be covered", stamp: 1, cutover: "active", parentOtherProvider: true, provenance: true},
		{name: "positive visible lane with absent cutover fails closed", positiveLane: true, provenance: true},
		{name: "positive visible lane with shadow fails closed", cutover: "shadow", positiveLane: true, provenance: true},
		{name: "unsupported capability provider fails closed", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) {
			row.resumabilityCapability = json.RawMessage(strings.Replace(string(row.resumabilityCapability), `"codex"`, `"synthetic"`, 1))
		}, provenance: true},
		{name: "different valid capability provider fails closed", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) {
			row.resumabilityCapability = json.RawMessage(strings.Replace(string(row.resumabilityCapability), `"codex"`, `"claude_code"`, 1))
		}, provenance: true},
		{name: "positive parent without provenance", stamp: 1, provenance: true},
		{name: "positive parent with shadow", stamp: 1, cutover: "shadow", provenance: true},
		{name: "positive parent still normalizing", stamp: 1, cutover: "normalizing", provenance: true},
		{name: "active cutover contradicts zero parent", cutover: "active", provenance: true},
		{name: "unsupported future parent stamp", stamp: 2, cutover: "active", provenance: true},
		{name: "normalizing requires receipt envelope", cutover: "normalizing", mutate: func(t *testing.T, row *lifecycleCutoverObservation) { row.normalization = nil }, provenance: true},
		{name: "active requires activation timestamp", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) { row.activatedAt = nil }, provenance: true},
		{name: "active requires minimum writer", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) { row.minimumWriterVersion = nil }, provenance: true},
		{name: "active rejects writer zero", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) {
			row.minimumWriterVersion = requestLifecycleScopePointer(0)
		}, provenance: true},
		{name: "active rejects unsupported writer minimum", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) {
			row.minimumWriterVersion = requestLifecycleScopePointer(2)
		}, provenance: true},
		{name: "active requires retained cutoff", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) { row.legacyInputCutoffID = nil }, provenance: true},
		{name: "active requires normalization", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) { row.normalization = nil }, provenance: true},
		{name: "active rejects malformed normalization shape", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) {
			row.normalization = json.RawMessage(`{"complete":true}`)
		}, provenance: true},
		{name: "active rejects future normalization schema", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) {
			row.normalization = lifecycleProtocolJSONField(t, row.normalization, "schema_version", 2)
		}, provenance: true},
		{name: "active requires complete normalization", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) {
			row.normalization = lifecycleProtocolJSONField(t, row.normalization, "complete", false)
		}, provenance: true},
		{name: "active requires matching cutoff", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) {
			row.normalization = lifecycleProtocolJSONField(t, row.normalization, "cutoff_id", 2)
		}, provenance: true},
		{name: "active requires floor receipt", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) {
			row.normalization = lifecycleProtocolJSONField(t, row.normalization, "floor_receipt_id", uuid.Nil)
		}, provenance: true},
		{name: "active requires capability", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) { row.resumabilityCapability = nil }, provenance: true},
		{name: "active rejects future capability schema", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) {
			row.resumabilityCapability = lifecycleProtocolJSONField(t, row.resumabilityCapability, "schema_version", 2)
		}, provenance: true},
		{name: "active requires capability tuples", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) {
			row.resumabilityCapability = lifecycleProtocolJSONField(t, row.resumabilityCapability, "tuples", []any{})
		}, provenance: true},
		{name: "active requires release receipt", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) {
			row.resumabilityCapability = lifecycleProtocolJSONField(t, row.resumabilityCapability, "release_receipt_id", uuid.Nil)
		}, provenance: true},
		{name: "active requires exact capability identity", stamp: 1, cutover: "active", mutate: func(t *testing.T, row *lifecycleCutoverObservation) {
			row.resumabilityCapability = json.RawMessage(strings.Replace(string(row.resumabilityCapability), "synthetic-cli-v1", "*", 1))
		}, provenance: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, _ := seedRuntimeSeedThread(t, pool)
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			writer := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, writer), "configure the synthetic fixture owner before seeding protocol metadata")
			require.NoError(t, tryLockLifecycleCutoverExclusiveInTx(ctx, writer, orgID, sessionID), "install each fixture atomically under its exact exclusive interlock")
			if tt.cutover != "" {
				row := lifecycleProtocolCutoverRow(t, fixture.cutover, orgID, sessionID, tt.cutover, tt.revision)
				if tt.mutate != nil {
					tt.mutate(t, &row)
				}
				// Deliberately malformed envelopes are inserted while the retained
				// parent is still zero. No guard is disabled for negative fixtures.
				insertLifecycleCutoverPostgresRow(t, ctx, writer, row)
			}
			if tt.stamp != 0 {
				_, err := writer.Exec(ctx, `UPDATE sessions SET lifecycle_version=$3 WHERE org_id=$1 AND id=$2`, orgID, sessionID, tt.stamp)
				require.NoError(t, err, "retain the positive parent stamp after constructing the negative fixture")
			}
			if (tt.stamp == 1 && tt.cutover == "active" && !tt.laneZero) || tt.positiveLane {
				_, err := writer.Exec(ctx, `UPDATE session_threads SET lifecycle_version=1 WHERE org_id=$1 AND session_id=$2 AND archived_at IS NULL`, orgID, sessionID)
				require.NoError(t, err, "synthetic activation must stamp every visible lane without inferring a primary target")
			}
			if tt.laneOtherProvider {
				_, err := writer.Exec(ctx, `UPDATE session_threads SET agent_type=$3 WHERE org_id=$1 AND session_id=$2`, orgID, sessionID, models.AgentTypeClaudeCode)
				require.NoError(t, err, "represent a visible lane whose actual provider lacks a tested tuple")
			}
			if tt.parentOtherProvider {
				_, err := writer.Exec(ctx, `UPDATE sessions SET agent_type=$3 WHERE org_id=$1 AND id=$2`, orgID, sessionID, models.AgentTypeClaudeCode)
				require.NoError(t, err, "represent a parent whose actual provider lacks a tested tuple")
			}
			require.NoError(t, writer.Commit(ctx), "commit only the complete synthetic matrix fixture")
			before := lifecycleProtocolRows(t, context.Background(), pool, orgID, sessionID)
			actor := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, actor), "configure the independent resolver transaction")
			actual, err := resolveLifecycleModeInTx(ctx, actor, orgID, sessionID)
			if tt.provenance {
				require.ErrorIs(t, err, ErrLifecycleGuardProvenanceRequired, "inconsistent retained evidence must fail closed rather than route to legacy")
				var provenance *LifecycleGuardProvenanceError
				require.ErrorAs(t, err, &provenance, "the stable sentinel must preserve its typed protocol cause")
			} else {
				require.NoError(t, err, "the exact supported parent and cutover state must resolve without an activation claim")
			}
			require.Equal(t, tt.want, actual, "resolution must return the exact mode and independent protocol and CAS versions or zero on error")
			require.Equal(t, before, lifecycleProtocolRows(t, ctx, actor, orgID, sessionID), "resolution must preserve every persisted parent and cutover field")
		})
	}
}

func TestLifecycleProtocolActivatedFixtureAndMissingScopePostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	tests := []struct {
		name       string
		scope      string
		want       lifecycleModeObservation
		wantNoRows bool
	}{
		{name: "shared activated fixture", want: lifecycleModeObservation{mode: lifecycleModeActive, protocolVersion: 1, cutoverVersion: 3}},
		{name: "soft deleted retained parent", scope: "soft_deleted", want: lifecycleModeObservation{mode: lifecycleModeActive, protocolVersion: 1, cutoverVersion: 3}},
		{name: "archived lane is not a runnable cohort member", scope: "archived_lane", want: lifecycleModeObservation{mode: lifecycleModeActive, protocolVersion: 1, cutoverVersion: 3}},
		{name: "missing parent", scope: "missing", wantNoRows: true},
		{name: "foreign tenant parent", scope: "foreign", wantNoRows: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := seedActivatedLifecycleFixture(t, context.Background(), pool)
			if tt.scope == "soft_deleted" {
				_, err := pool.Exec(context.Background(), `UPDATE sessions SET deleted_at=now() WHERE org_id=$1 AND id=$2`, fixture.orgID, fixture.sessionID)
				require.NoError(t, err, "soft deletion must retain active protocol provenance")
			}
			if tt.scope == "archived_lane" {
				_, err := pool.Exec(context.Background(), `UPDATE session_threads SET archived_at=now(),lifecycle_version=0,agent_type=$4 WHERE org_id=$1 AND session_id=$2 AND id=$3`, fixture.orgID, fixture.sessionID, fixture.threadID, models.AgentTypeClaudeCode)
				require.NoError(t, err, "archived lane metadata must remain observable without joining the visible active cohort")
			}
			orgID, sessionID := fixture.orgID, fixture.sessionID
			if tt.scope == "missing" {
				sessionID = uuid.New()
			} else if tt.scope == "foreign" {
				orgID = uuid.New()
			}
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			actor := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, actor), "configure the exact-scope reader without granting cross-tenant authority")
			actual, err := resolveLifecycleModeInTx(ctx, actor, orgID, sessionID)
			if tt.wantNoRows {
				require.ErrorIs(t, err, pgx.ErrNoRows, "only an exact tenant-scoped parent can supply protocol provenance")
				require.NotErrorIs(t, err, ErrLifecycleGuardProvenanceRequired, "an absent parent must remain distinct from missing active evidence")
			} else {
				require.NoError(t, err, "the common frozen active fixture must remain observable after ordinary soft deletion")
			}
			require.Equal(t, tt.want, actual, "foreign or missing scope must expose no partial active observation")
		})
	}
}

func TestLifecycleProtocolFreshStatementPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	fixture := seedActivatedLifecycleFixture(t, context.Background(), pool)
	tests := []struct {
		name   string
		shadow bool
	}{
		{name: "old absence cannot hide later activation"},
		{name: "old shadow cannot hide later activation", shadow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, _ := seedRuntimeSeedThread(t, pool)
			if tt.shadow {
				insertLifecycleCutoverPostgresRow(t, context.Background(), pool, lifecycleCutoverObservation{orgID: orgID, sessionID: sessionID, state: models.LifecycleCutoverStateShadow})
			}
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			actor := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, actor), "start the READ COMMITTED actor before the independent synthetic writer")
			identity := lifecycleCutoverPostgresTransactionIdentity(t, ctx, actor)
			var oldStamp int16
			var oldState *models.LifecycleCutoverState
			require.NoError(t, actor.QueryRow(ctx, `SELECT s.lifecycle_version,c.state FROM sessions s LEFT JOIN session_lifecycle_cutovers c ON c.org_id=s.org_id AND c.session_id=s.id WHERE s.org_id=$1 AND s.id=$2`, orgID, sessionID).Scan(&oldStamp, &oldState), "the actor's earlier plain statement must observe the preactivation parent and optional shadow")
			var expectedState *models.LifecycleCutoverState
			if tt.shadow {
				state := models.LifecycleCutoverStateShadow
				expectedState = &state
			}
			require.Equal(t, int16(0), oldStamp, "the earlier statement must precede the positive parent stamp")
			require.Equal(t, expectedState, oldState, "the earlier statement must actually see shadow or absence rather than active provenance")
			activator := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, activator), "configure the separate synthetic metadata owner")
			require.NoError(t, tryLockLifecycleCutoverExclusiveInTx(ctx, activator, orgID, sessionID), "an earlier plain read must not hold the shared protocol interlock")
			if tt.shadow {
				_, err := activator.Exec(ctx, `DELETE FROM session_lifecycle_cutovers WHERE org_id=$1 AND session_id=$2`, orgID, sessionID)
				require.NoError(t, err, "replace only inert never-activated shadow fixture metadata")
			}
			insertLifecycleCutoverPostgresRow(t, ctx, activator, lifecycleProtocolCutoverRow(t, fixture.cutover, orgID, sessionID, "active", 17))
			_, err := activator.Exec(ctx, `UPDATE sessions SET lifecycle_version=1 WHERE org_id=$1 AND id=$2`, orgID, sessionID)
			require.NoError(t, err, "stage the matching positive parent stamp in the same synthetic transaction")
			_, err = activator.Exec(ctx, `UPDATE session_threads SET lifecycle_version=1 WHERE org_id=$1 AND session_id=$2 AND archived_at IS NULL`, orgID, sessionID)
			require.NoError(t, err, "stage every visible lane stamp before the synthetic active commit")
			require.NoError(t, activator.Commit(ctx), "commit full cutover provenance and stamp before the late resolver acquisition")
			wrapper := &lifecycleScopeConcurrencyTx{Tx: actor}
			actual, err := resolveLifecycleModeInTx(ctx, wrapper, orgID, sessionID)
			require.NoError(t, err, "late shared acquisition must continue to a fresh observation in the existing transaction")
			require.Equal(t, lifecycleModeObservation{mode: lifecycleModeActive, protocolVersion: 1, cutoverVersion: 17}, actual, "the resolver must see the complete activation committed after its earlier statement")
			require.Equal(t, identity, lifecycleCutoverPostgresTransactionIdentity(t, ctx, actor), "fresh visibility must preserve the actor's backend and transaction identity")
			requireLifecycleScopeNoSettlement(t, wrapper)
		})
	}
}

func TestLifecycleProtocolInterlockLifetimePostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	tests := []struct {
		name   string
		commit bool
	}{{name: "outer rollback releases shared lock"}, {name: "outer commit releases shared lock", commit: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := seedActivatedLifecycleFixture(t, context.Background(), pool)
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			actor := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, actor), "configure the resolver's outer owner")
			actual, err := resolveLifecycleModeInTx(ctx, actor, fixture.orgID, fixture.sessionID)
			require.NoError(t, err, "resolve supported active provenance while retaining the caller's shared lock")
			require.Equal(t, lifecycleModeObservation{mode: lifecycleModeActive, protocolVersion: 1, cutoverVersion: 3}, actual, "successful resolution must preserve both independent retained versions")
			contender := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, contender), "configure the independent exclusive contender")
			err = tryLockLifecycleCutoverExclusiveInTx(ctx, contender, fixture.orgID, fixture.sessionID)
			requireLifecycleCutoverPostgresBusy(t, err)
			require.NotErrorIs(t, err, ErrLifecycleGuardProvenanceRequired, "a physically busy interlock is not malformed or missing provenance")
			require.Equal(t, byte('T'), contender.Conn().PgConn().TxStatus(), "a false advisory try-lock must leave settlement with the contender owner")
			foreign := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, foreign), "configure the separate tenant-isolation probe")
			require.NoError(t, tryLockLifecycleCutoverExclusiveInTx(ctx, foreign, uuid.New(), fixture.sessionID), "a different tenant key must not contend with the exact active scope")
			if tt.commit {
				require.NoError(t, actor.Commit(ctx), "only the outer owner may release the resolver's shared lock by commit")
			} else {
				require.NoError(t, actor.Rollback(ctx), "only the outer owner may release the resolver's shared lock by rollback")
			}
			require.NoError(t, tryLockLifecycleCutoverExclusiveInTx(ctx, contender, fixture.orgID, fixture.sessionID), "the same exclusive contender must acquire immediately after outer settlement")
		})
	}
}

func TestLifecycleProtocolBusyAndObservationErrorsPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	tests := []struct {
		name, failure string
		wantCanceled  bool
	}{
		{name: "actual exclusive conflict", failure: "busy"},
		{name: "real observation query error", failure: "query"},
		{name: "scan fails after partial parent", failure: "scan"},
		{name: "cancel before acquisition", failure: "before", wantCanceled: true},
		{name: "cancel after successful scan", failure: "after", wantCanceled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := seedActivatedLifecycleFixture(t, context.Background(), pool)
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			actor := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, actor), "configure an actual PostgreSQL transaction before inducing the selected boundary error")
			wrapper := &lifecycleProtocolObservationTx{Tx: actor, failure: tt.failure}
			if tt.failure == "busy" {
				holder := beginSessionLifecycleProbe(t, ctx, pool)
				require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, holder), "configure the independent actual exclusive blocker")
				require.NoError(t, tryLockLifecycleCutoverExclusiveInTx(ctx, holder, fixture.orgID, fixture.sessionID), "hold a real exclusive interlock through the resolver call")
			} else if tt.failure == "before" {
				cancel()
			} else if tt.failure == "after" {
				wrapper.afterScan = cancel
			}
			actual, err := resolveLifecycleModeInTx(ctx, wrapper, fixture.orgID, fixture.sessionID)
			require.Error(t, err, "every selected transaction or observation failure must be returned to the owner")
			require.Equal(t, lifecycleModeObservation{}, actual, "no error may expose an already scanned or previously available active observation")
			require.NotErrorIs(t, err, ErrLifecycleGuardProvenanceRequired, "database errors cancellation and contention must retain their distinct cause")
			if tt.failure == "busy" {
				requireLifecycleCutoverPostgresBusy(t, err)
			}
			if tt.wantCanceled {
				require.ErrorIs(t, err, context.Canceled, "cancellation must survive wrapper boundaries")
			}
			wantQueries := 1
			if tt.failure == "busy" || tt.failure == "before" {
				wantQueries = 0
			}
			require.Equal(t, wantQueries, wrapper.observationQueries, "failed entry must not read provenance and observation failures must execute exactly one fresh statement")
		})
	}
}

func TestLifecycleProtocolRetentionGuardsPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	tests := []struct{ name, query string }{
		{name: "positive parent cannot downgrade", query: `UPDATE sessions SET lifecycle_version=0 WHERE org_id=$1 AND id=$2`},
		{name: "positive parent cannot raw delete", query: `DELETE FROM sessions WHERE org_id=$1 AND id=$2`},
		{name: "positive parent cannot move identity", query: `UPDATE sessions SET id=$3 WHERE org_id=$1 AND id=$2`},
		{name: "positive parent cannot move tenant", query: `UPDATE sessions SET org_id=$3 WHERE org_id=$1 AND id=$2`},
		{name: "active cutover cannot delete", query: `DELETE FROM session_lifecycle_cutovers WHERE org_id=$1 AND session_id=$2`},
		{name: "active cutover cannot move session", query: `UPDATE session_lifecycle_cutovers SET session_id=$3 WHERE org_id=$1 AND session_id=$2`},
		{name: "active cutover cannot move tenant", query: `UPDATE session_lifecycle_cutovers SET org_id=$3 WHERE org_id=$1 AND session_id=$2`},
		{name: "active cutover cannot regress to shadow", query: `UPDATE session_lifecycle_cutovers SET state='shadow' WHERE org_id=$1 AND session_id=$2`},
		{name: "active cutover cannot regress to normalizing", query: `UPDATE session_lifecycle_cutovers SET state='normalizing' WHERE org_id=$1 AND session_id=$2`},
		{name: "active cutover cannot regress CAS revision", query: `UPDATE session_lifecycle_cutovers SET version=0 WHERE org_id=$1 AND session_id=$2`},
		{name: "active cutover cannot clear activation", query: `UPDATE session_lifecycle_cutovers SET activated_at=NULL WHERE org_id=$1 AND session_id=$2`},
		{name: "active cutover cannot clear cutoff", query: `UPDATE session_lifecycle_cutovers SET legacy_input_cutoff_id=NULL WHERE org_id=$1 AND session_id=$2`},
		{name: "active cutover cannot clear normalization", query: `UPDATE session_lifecycle_cutovers SET normalization=NULL WHERE org_id=$1 AND session_id=$2`},
		{name: "active cutover cannot overwrite normalization", query: `UPDATE session_lifecycle_cutovers SET normalization='{}'::jsonb WHERE org_id=$1 AND session_id=$2`},
		{name: "active cutover cannot rewrite normalization receipt", query: `UPDATE session_lifecycle_cutovers SET normalization=jsonb_set(normalization,'{receipt_id}',to_jsonb(gen_random_uuid())) WHERE org_id=$1 AND session_id=$2`},
		{name: "active cutover cannot change normalization cutoff numeric encoding", query: `UPDATE session_lifecycle_cutovers SET normalization=jsonb_set(normalization,'{cutoff_id}',to_jsonb((normalization->>'cutoff_id')::numeric + 0.0)) WHERE org_id=$1 AND session_id=$2`},
		{name: "active cutover cannot change normalization schema numeric encoding", query: `UPDATE session_lifecycle_cutovers SET normalization=jsonb_set(normalization,'{schema_version}','1.00'::jsonb) WHERE org_id=$1 AND session_id=$2`},
		{name: "active cutover cannot clear capability", query: `UPDATE session_lifecycle_cutovers SET resumability_capability=NULL WHERE org_id=$1 AND session_id=$2`},
		{name: "active cutover cannot clear minimum", query: `UPDATE session_lifecycle_cutovers SET minimum_writer_version=NULL WHERE org_id=$1 AND session_id=$2`},
		{name: "active cutover cannot lower minimum", query: `UPDATE session_lifecycle_cutovers SET minimum_writer_version=0 WHERE org_id=$1 AND session_id=$2`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := seedActivatedLifecycleFixture(t, context.Background(), pool)
			before := lifecycleProtocolRows(t, context.Background(), pool, fixture.orgID, fixture.sessionID)
			args := []any{fixture.orgID, fixture.sessionID}
			if strings.Contains(tt.query, "$3") {
				args = append(args, uuid.New())
			}
			_, err := pool.Exec(context.Background(), tt.query, args...)
			requireSessionLifecycleSQLState(t, err, "55000", "retained active evidence must reject destructive regression before unrelated FKs or checks")
			require.ErrorContains(t, err, "LIFECYCLE_GUARD_PROVENANCE_REQUIRED", "SQL retention failures must use the stable fail-closed protocol category")
			require.Equal(t, before, lifecycleProtocolRows(t, context.Background(), pool, fixture.orgID, fixture.sessionID), "a rejected mutation must preserve the complete committed parent and cutover")
		})
	}
}

func TestLifecycleProtocolNormalizationExactNoopPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	tests := []struct{ name, expression string }{
		{name: "self assignment", expression: "normalization"},
		{name: "key order is not an alternate encoding", expression: `(SELECT jsonb_object_agg(key,value ORDER BY key DESC) FROM jsonb_each(normalization))`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := seedActivatedLifecycleFixture(t, context.Background(), pool)
			before := lifecycleProtocolRows(t, context.Background(), pool, fixture.orgID, fixture.sessionID)
			_, err := pool.Exec(context.Background(), `UPDATE session_lifecycle_cutovers SET normalization=`+tt.expression+` WHERE org_id=$1 AND session_id=$2`, fixture.orgID, fixture.sessionID)
			require.NoError(t, err, "an exact no-op must preserve the retained active normalization receipt without requiring replacement")
			require.Equal(t, before, lifecycleProtocolRows(t, context.Background(), pool, fixture.orgID, fixture.sessionID), "a permitted exact no-op must preserve every retained field")
		})
	}
}

func TestLifecycleProtocolExplicitHardDeleteRefusedPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	tests := []struct {
		name    string
		cutover bool
	}{
		{name: "complete active provenance", cutover: true},
		{name: "missing active cutover"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var orgID, sessionID uuid.UUID
			if tt.cutover {
				fixture := seedActivatedLifecycleFixture(t, context.Background(), pool)
				orgID, sessionID = fixture.orgID, fixture.sessionID
			} else {
				orgID, sessionID, _ = seedRuntimeSeedThread(t, pool)
				_, err := pool.Exec(context.Background(), `UPDATE sessions SET lifecycle_version=1 WHERE org_id=$1 AND id=$2`, orgID, sessionID)
				require.NoError(t, err, "retain a positive protocol stamp independently of optional cutover evidence")
			}
			before := lifecycleProtocolRows(t, context.Background(), pool, orgID, sessionID)
			tx := beginSessionLifecycleProbe(t, context.Background(), pool)
			_, err := tx.Exec(context.Background(), `SET LOCAL app.allow_hard_delete = 'true'`)
			require.NoError(t, err, "exercise the actual existing hard-delete opt-in rather than the default soft-delete redirect")
			_, err = tx.Exec(context.Background(), `DELETE FROM sessions WHERE org_id=$1 AND id=$2`, orgID, sessionID)
			requireSessionLifecycleSQLState(t, err, "55000", "the explicit legacy hard-delete option cannot erase retained positive protocol history")
			require.NoError(t, tx.Rollback(context.Background()), "discard the failed opt-in deletion before independently reading retained evidence")
			require.Equal(t, before, lifecycleProtocolRows(t, context.Background(), pool, orgID, sessionID), "hard-delete refusal must preserve every parent and cutover field")
		})
	}
}

func TestLifecycleProtocolLegacySnapshotWritesPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	type deletionResult struct {
		Affected    int64
		Remaining   int
		SoftDeleted bool
	}
	tests := []struct {
		name       string
		isolation  pgx.TxIsoLevel
		hardDelete bool
		want       deletionResult
	}{
		{name: "read committed soft delete", isolation: pgx.ReadCommitted, want: deletionResult{0, 1, true}},
		{name: "repeatable read soft delete", isolation: pgx.RepeatableRead, want: deletionResult{0, 1, true}},
		{name: "serializable soft delete", isolation: pgx.Serializable, want: deletionResult{0, 1, true}},
		{name: "read committed explicit hard delete", isolation: pgx.ReadCommitted, hardDelete: true, want: deletionResult{1, 0, false}},
		{name: "repeatable read explicit hard delete", isolation: pgx.RepeatableRead, hardDelete: true, want: deletionResult{1, 0, false}},
		{name: "serializable explicit hard delete", isolation: pgx.Serializable, hardDelete: true, want: deletionResult{1, 0, false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, _ := seedRuntimeSeedThread(t, pool)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: tt.isolation})
			require.NoError(t, err, "begin the characterized version-zero writer without installing a version-one policy")
			defer func() { _ = tx.Rollback(context.Background()) }()
			_, err = tx.Exec(ctx, `UPDATE sessions SET title='Synthetic version-zero update' WHERE org_id=$1 AND id=$2`, orgID, sessionID)
			require.NoError(t, err, "inactive parent updates must preserve characterized isolation compatibility")
			if tt.hardDelete {
				_, err = tx.Exec(ctx, `SET LOCAL app.allow_hard_delete = 'true'`)
				require.NoError(t, err, "use the existing explicit transaction-local opt-in rather than changing the default soft-delete behavior")
			}
			tag, err := tx.Exec(ctx, `DELETE FROM sessions WHERE org_id=$1 AND id=$2`, orgID, sessionID)
			require.NoError(t, err, "a never-activated session must preserve both ordinary soft deletion and explicitly opted-in hard deletion")
			require.NoError(t, tx.Commit(ctx), "commit the complete legacy update/delete transaction under its original isolation")
			actual := deletionResult{Affected: tag.RowsAffected()}
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*), COALESCE(bool_and(deleted_at IS NOT NULL AND lifecycle_version=0 AND title='Synthetic version-zero update'), false) FROM sessions WHERE org_id=$1 AND id=$2`, orgID, sessionID).Scan(&actual.Remaining, &actual.SoftDeleted), "read back the exact committed legacy deletion result independently of the writer")
			require.Equal(t, tt.want, actual, "default legacy deletion must retain its tombstone while explicit hard deletion removes the inactive parent")
		})
	}
}

func TestLifecycleProtocolPositiveParentWithoutCutoverRetainedPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	tests := []struct{ name, query string }{
		{name: "missing cutover cannot permit stamp downgrade", query: `UPDATE sessions SET lifecycle_version=0 WHERE org_id=$1 AND id=$2`},
		{name: "missing cutover cannot permit raw parent deletion", query: `DELETE FROM sessions WHERE org_id=$1 AND id=$2`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, _ := seedRuntimeSeedThread(t, pool)
			_, err := pool.Exec(context.Background(), `UPDATE sessions SET lifecycle_version=1 WHERE org_id=$1 AND id=$2`, orgID, sessionID)
			require.NoError(t, err, "seed retained positive provenance without manufacturing an active cutover")
			before := lifecycleProtocolRows(t, context.Background(), pool, orgID, sessionID)
			_, err = pool.Exec(context.Background(), tt.query, orgID, sessionID)
			requireSessionLifecycleSQLState(t, err, "55000", "missing cutover must not erase the parent's positive protocol history")
			require.Equal(t, before, lifecycleProtocolRows(t, context.Background(), pool, orgID, sessionID), "rejection must retain the positive parent even when provenance is incomplete")
		})
	}
}

func TestLifecycleProtocolEitherRetainedEvidenceGuardsCutoverPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	template := seedActivatedLifecycleFixture(t, context.Background(), pool)
	tests := []struct {
		name, state, query string
		stamp              int16
	}{
		{name: "active cutover with zero parent cannot delete", state: "active", query: `DELETE FROM session_lifecycle_cutovers WHERE org_id=$1 AND session_id=$2`},
		{name: "active cutover with zero parent cannot reset", state: "active", query: `UPDATE session_lifecycle_cutovers SET state='shadow' WHERE org_id=$1 AND session_id=$2`},
		{name: "positive parent with shadow cannot delete", state: "shadow", stamp: 1, query: `DELETE FROM session_lifecycle_cutovers WHERE org_id=$1 AND session_id=$2`},
		{name: "positive parent with shadow cannot reset", state: "shadow", stamp: 1, query: `UPDATE session_lifecycle_cutovers SET version=0 WHERE org_id=$1 AND session_id=$2`},
		{name: "positive parent cannot accept new shadow", stamp: 1, query: `INSERT INTO session_lifecycle_cutovers(org_id,session_id,version,state) VALUES($1,$2,0,'shadow')`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, _ := seedRuntimeSeedThread(t, pool)
			if tt.state != "" {
				insertLifecycleCutoverPostgresRow(t, context.Background(), pool, lifecycleProtocolCutoverRow(t, template.cutover, orgID, sessionID, tt.state, 17))
			}
			if tt.stamp != 0 {
				_, err := pool.Exec(context.Background(), `UPDATE sessions SET lifecycle_version=$3 WHERE org_id=$1 AND id=$2`, orgID, sessionID, tt.stamp)
				require.NoError(t, err, "retain positive parent evidence after inserting the deliberately contradictory shadow fixture")
			}
			before := lifecycleProtocolRows(t, context.Background(), pool, orgID, sessionID)
			_, err := pool.Exec(context.Background(), tt.query, orgID, sessionID)
			requireSessionLifecycleSQLState(t, err, "55000", "either retained active cutover or positive parent evidence must independently prevent provenance loss")
			require.ErrorContains(t, err, "LIFECYCLE_GUARD_PROVENANCE_REQUIRED", "contradictory retained evidence must not reopen version-zero mutation semantics")
			require.Equal(t, before, lifecycleProtocolRows(t, context.Background(), pool, orgID, sessionID), "the guard must preserve even incomplete or contradictory retained evidence for fail-closed resolution")
		})
	}
}

func TestLifecycleProtocolSQLInterlockPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	tests := []struct {
		name, query string
	}{
		{name: "parent update checks the exact interlock", query: `UPDATE sessions SET title='Synthetic blocked update' WHERE org_id=$1 AND id=$2`},
		{name: "cutover update checks the exact interlock", query: `UPDATE session_lifecycle_cutovers SET version=version+1 WHERE org_id=$1 AND session_id=$2`},
		{name: "cutover delete checks interlock before provenance", query: `DELETE FROM session_lifecycle_cutovers WHERE org_id=$1 AND session_id=$2`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture := seedActivatedLifecycleFixture(t, context.Background(), pool)
			before := lifecycleProtocolRows(t, context.Background(), pool, fixture.orgID, fixture.sessionID)
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			holder := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, holder), "configure an independent synthetic exclusive holder")
			require.NoError(t, tryLockLifecycleCutoverExclusiveInTx(ctx, holder, fixture.orgID, fixture.sessionID), "hold the actual Go protocol key while the SQL trigger runs")
			actor := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, actor), "configure the independent SQL mutation owner")
			_, err := actor.Exec(ctx, tt.query, fixture.orgID, fixture.sessionID)
			requireSessionLifecycleSQLState(t, err, "55P03", "SQL guards must use the same real nonwaiting key as the Go resolver")
			require.ErrorContains(t, err, "LIFECYCLE_CUTOVER_INTERLOCK_BUSY", "only actual exclusive conflict may produce the SQL busy category")
			require.Equal(t, byte('E'), actor.Conn().PgConn().TxStatus(), "SQL trigger refusal must abort the mutation transaction for whole-unit rollback")
			require.NoError(t, actor.Rollback(ctx), "the mutation owner must discard its aborted whole transaction")
			require.Equal(t, before, lifecycleProtocolRows(t, ctx, holder, fixture.orgID, fixture.sessionID), "a nonwaiting SQL refusal must commit no parent or cutover mutation")
		})
	}
}

func TestLifecycleProtocolSQLFreshLookupPostgres(t *testing.T) {
	t.Parallel()
	sourcePool := newLifecycleScopeSnapshotPool(t)
	template := seedActivatedLifecycleFixture(t, context.Background(), sourcePool)
	tests := []struct {
		name, query string
		oldShadow   bool
	}{
		{name: "late insert sees complete active provenance", query: `INSERT INTO session_lifecycle_cutovers(org_id,session_id,version,state) VALUES($1,$2,0,'shadow')`},
		{name: "late update sees retained positive parent", query: `UPDATE session_lifecycle_cutovers SET version=version+1 WHERE org_id=$1 AND session_id=$2`, oldShadow: true},
		{name: "late delete sees retained positive parent", query: `DELETE FROM session_lifecycle_cutovers WHERE org_id=$1 AND session_id=$2`, oldShadow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			orgID, sessionID, _ := seedRuntimeSeedThread(t, pool)
			if tt.oldShadow {
				insertLifecycleCutoverPostgresRow(t, context.Background(), pool, lifecycleCutoverObservation{orgID: orgID, sessionID: sessionID, state: models.LifecycleCutoverStateShadow})
			}
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			actor := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, actor), "begin the mutation owner before the independent positive-stamp commit")
			actorPID := backendPID(t, ctx, actor)
			// An earlier alphabetically ordered test-only trigger blocks this
			// backend before the shipped retention trigger. Its advisory wait is
			// bounded by the existing statement/caller deadlines; disabling only
			// this barrier function's lock timeout avoids a scheduler race. It
			// changes no production function, guard or transaction setting.
			_, err := pool.Exec(ctx, `CREATE FUNCTION lifecycle_protocol_test_statement_barrier()
				RETURNS trigger LANGUAGE plpgsql SET lock_timeout='0' AS $$
				BEGIN
					IF pg_backend_pid()=TG_ARGV[0]::integer THEN
						PERFORM pg_advisory_xact_lock(TG_ARGV[1]::bigint);
					END IF;
					IF TG_OP='DELETE' THEN RETURN OLD; END IF;
					RETURN NEW;
				END; $$`)
			require.NoError(t, err, "install a physical statement barrier only inside this owned disposable clone")
			const barrierKey int64 = 7345123
			_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER aaa_lifecycle_protocol_test_statement_barrier BEFORE INSERT OR UPDATE OR DELETE ON session_lifecycle_cutovers FOR EACH ROW EXECUTE FUNCTION lifecycle_protocol_test_statement_barrier('%d','%d')`, actorPID, barrierKey))
			require.NoError(t, err, "target the barrier only at the exact independent mutation backend")
			barrier := beginSessionLifecycleProbe(t, ctx, pool)
			var acquired bool
			require.NoError(t, barrier.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, barrierKey).Scan(&acquired), "hold the unrelated test barrier before starting the mutation statement")
			require.True(t, acquired, "the test must own its barrier rather than rely on a timing delay")
			result, finished := make(chan error, 1), make(chan struct{})
			go func() {
				defer close(finished)
				_, mutationErr := actor.Exec(ctx, tt.query, orgID, sessionID)
				result <- mutationErr
			}()
			t.Cleanup(func() {
				cancel()
				cleanupCtx, stop := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
				defer stop()
				waitForResult(t, cleanupCtx, finished)
			})
			waitForBlockedBackend(t, ctx, barrier, actorPID)
			activator := beginSessionLifecycleProbe(t, ctx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, activator), "configure the independent synthetic provenance writer after the old statement has begun")
			require.NoError(t, tryLockLifecycleCutoverExclusiveInTx(ctx, activator, orgID, sessionID), "the actor must not acquire the production shared key before its physical barrier")
			if !tt.oldShadow {
				insertLifecycleCutoverPostgresRow(t, ctx, activator, lifecycleProtocolCutoverRow(t, template.cutover, orgID, sessionID, "active", 17))
			}
			_, err = activator.Exec(ctx, `UPDATE sessions SET lifecycle_version=1 WHERE org_id=$1 AND id=$2`, orgID, sessionID)
			require.NoError(t, err, "commit positive parent evidence after the mutation statement's initial snapshot")
			if !tt.oldShadow {
				_, err = activator.Exec(ctx, `UPDATE session_threads SET lifecycle_version=1 WHERE org_id=$1 AND session_id=$2 AND archived_at IS NULL`, orgID, sessionID)
				require.NoError(t, err, "full synthetic insertion activation must include all visible lane stamps")
			}
			// UPDATE/DELETE already own the old shadow row, so their independent
			// writer deliberately stamps only the parent. That incomplete state
			// is itself retained evidence, never a successful activation claim.
			require.NoError(t, activator.Commit(ctx), "release the production exclusive key only after the new provenance is committed")
			before := lifecycleProtocolRows(t, ctx, barrier, orgID, sessionID)
			require.NoError(t, barrier.Rollback(ctx), "release the physical barrier only after independent committed evidence is visible")
			err = waitForResult(t, ctx, result)
			waitForResult(t, ctx, finished)
			requireSessionLifecycleSQLState(t, err, "55000", "the shipped VOLATILE trigger must see post-interlock positive evidence from a newer statement snapshot")
			require.ErrorContains(t, err, "LIFECYCLE_GUARD_PROVENANCE_REQUIRED", "late discovery must reject retained evidence rather than accept stale version zero or fail only on uniqueness")
			require.NoError(t, actor.Rollback(ctx), "the old mutation owner must discard the entire rejected statement transaction")
			require.Equal(t, before, lifecycleProtocolRows(t, ctx, pool, orgID, sessionID), "late rejection must preserve the independent writer's exact committed evidence")
		})
	}
}

func TestLifecycleProtocolTruncateRefusalPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, query string
	}{
		{name: "active cutover truncate", query: `TRUNCATE session_lifecycle_cutovers`},
		{name: "positive parent truncate with dependent tables", query: `TRUNCATE sessions CASCADE`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Each destructive statement is confined to its own disposable clone;
			// no other test's tenants or database can enter this target set.
			pool := newLifecycleScopeSnapshotPool(t)
			fixture := seedActivatedLifecycleFixture(t, context.Background(), pool)
			before := lifecycleProtocolRows(t, context.Background(), pool, fixture.orgID, fixture.sessionID)
			_, err := pool.Exec(context.Background(), tt.query)
			requireSessionLifecycleSQLState(t, err, "55000", "TRUNCATE must not bypass retained positive protocol evidence")
			require.ErrorContains(t, err, "LIFECYCLE_GUARD_PROVENANCE_REQUIRED", "bulk retention refusal must expose the stable protocol category")
			require.Equal(t, before, lifecycleProtocolRows(t, context.Background(), pool, fixture.orgID, fixture.sessionID), "TRUNCATE rejection must retain every scoped parent and cutover field")
		})
	}
}

func TestLifecycleProtocolLegacyWritesRemainCompatiblePostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	orgID, sessionID, threadID := seedRuntimeSeedThread(t, pool)
	ctx := context.Background()
	insertLifecycleCutoverPostgresRow(t, ctx, pool, lifecycleCutoverObservation{orgID: orgID, sessionID: sessionID, state: models.LifecycleCutoverStateShadow, version: 17})
	_, err := pool.Exec(ctx, `UPDATE sessions SET title='Synthetic legacy update',status='running' WHERE org_id=$1 AND id=$2`, orgID, sessionID)
	require.NoError(t, err, "an unversioned version-zero parent writer must retain characterized coexistence")
	_, err = pool.Exec(ctx, `UPDATE session_threads SET label='Synthetic legacy lane update' WHERE org_id=$1 AND session_id=$2 AND id=$3`, orgID, sessionID, threadID)
	require.NoError(t, err, "the early provenance core must not prematurely install later child-writer guards")
	_, err = pool.Exec(ctx, `UPDATE session_lifecycle_cutovers SET version=0,normalization=NULL WHERE org_id=$1 AND session_id=$2`, orgID, sessionID)
	require.NoError(t, err, "inert never-activated shadow metadata may still reset under the protocol core")
	_, err = pool.Exec(ctx, `DELETE FROM session_lifecycle_cutovers WHERE org_id=$1 AND session_id=$2`, orgID, sessionID)
	require.NoError(t, err, "inert version-zero shadow metadata may be removed without fabricating active provenance")
	var title, status, label string
	var stamp int16
	var cutovers int
	err = pool.QueryRow(ctx, `SELECT s.title,s.status,s.lifecycle_version,t.label,(SELECT count(*) FROM session_lifecycle_cutovers c WHERE c.org_id=s.org_id AND c.session_id=s.id) FROM sessions s JOIN session_threads t ON t.org_id=s.org_id AND t.session_id=s.id AND t.id=$3 WHERE s.org_id=$1 AND s.id=$2`, orgID, sessionID, threadID).Scan(&title, &status, &stamp, &label, &cutovers)
	require.NoError(t, err, "read back every exact legacy parent lane and cutover outcome")
	require.Equal(t, []string{"Synthetic legacy update", "running", "Synthetic legacy lane update"}, []string{title, status, label}, "unversioned legacy updates must commit their exact requested values")
	require.Equal(t, int16(0), stamp, "legacy writes must preserve the never-activated parent stamp")
	require.Equal(t, 0, cutovers, "legacy shadow removal must leave no fabricated protocol row")
}

func TestLifecycleProtocolRetentionRollbackRefusalPostgres(t *testing.T) {
	t.Parallel()
	source := newLifecycleScopeSnapshotPool(t)
	template := seedActivatedLifecycleFixture(t, context.Background(), source)
	tests := []struct {
		name, migration string
		positiveOnly    bool
		activeOnly      bool
	}{
		{name: "function rollback retains guard dependencies", migration: "000346_lifecycle_protocol_retention_functions.down.sql"},
		{name: "parent guard rollback refuses positive evidence", migration: "000347_session_protocol_retention.down.sql"},
		{name: "cutover guard rollback refuses positive evidence", migration: "000348_cutover_protocol_retention.down.sql"},
		{name: "function rollback refuses missing active cutover", migration: "000346_lifecycle_protocol_retention_functions.down.sql", positiveOnly: true},
		{name: "parent guard rollback refuses missing active cutover", migration: "000347_session_protocol_retention.down.sql", positiveOnly: true},
		{name: "cutover guard rollback refuses missing active cutover", migration: "000348_cutover_protocol_retention.down.sql", positiveOnly: true},
		{name: "function rollback refuses active cutover alone", migration: "000346_lifecycle_protocol_retention_functions.down.sql", activeOnly: true},
		{name: "parent guard rollback refuses active cutover alone", migration: "000347_session_protocol_retention.down.sql", activeOnly: true},
		{name: "cutover guard rollback refuses active cutover alone", migration: "000348_cutover_protocol_retention.down.sql", activeOnly: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			var orgID, sessionID uuid.UUID
			if tt.activeOnly {
				orgID, sessionID, _ = seedRuntimeSeedThread(t, pool)
				insertLifecycleCutoverPostgresRow(t, context.Background(), pool, lifecycleProtocolCutoverRow(t, template.cutover, orgID, sessionID, "active", 3))
			} else if tt.positiveOnly {
				orgID, sessionID, _ = seedRuntimeSeedThread(t, pool)
				_, err := pool.Exec(context.Background(), `UPDATE sessions SET lifecycle_version=1 WHERE org_id=$1 AND id=$2`, orgID, sessionID)
				require.NoError(t, err, "seed retained positive evidence without any active cutover in this rollback database")
			} else {
				fixture := seedActivatedLifecycleFixture(t, context.Background(), pool)
				orgID, sessionID = fixture.orgID, fixture.sessionID
			}
			before := lifecycleProtocolRows(t, context.Background(), pool, orgID, sessionID)
			migration, err := os.ReadFile("../../migrations/" + tt.migration)
			require.NoError(t, err, "read the exact shipped down migration rather than duplicate its rollback policy")
			tx := beginSessionLifecycleProbe(t, context.Background(), pool)
			_, err = tx.Exec(context.Background(), string(migration))
			requireSessionLifecycleSQLState(t, err, "55000", "rollback must not remove guards or their functions while positive protocol evidence remains")
			require.ErrorContains(t, err, "LIFECYCLE_GUARD_PROVENANCE_REQUIRED", "rollback must refuse because of retained evidence rather than an incidental dependency error")
			require.NoError(t, tx.Rollback(context.Background()), "the rollback test owner must discard the failed migration transaction")
			require.Equal(t, before, lifecycleProtocolRows(t, context.Background(), pool, orgID, sessionID), "rollback refusal must retain all active evidence")
			query := `UPDATE sessions SET lifecycle_version=0 WHERE org_id=$1 AND id=$2`
			if tt.activeOnly {
				query = `DELETE FROM session_lifecycle_cutovers WHERE org_id=$1 AND session_id=$2`
			}
			_, err = pool.Exec(context.Background(), query, orgID, sessionID)
			requireSessionLifecycleSQLState(t, err, "55000", "the retained parent guard must remain installed after failed rollback")
		})
	}
}

func TestLifecycleProtocolInactiveRollbackRoundTripPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		shadow bool
	}{
		{name: "never activated without metadata"},
		{name: "never activated with inert shadow", shadow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			orgID, sessionID, _ := seedRuntimeSeedThread(t, pool)
			if tt.shadow {
				insertLifecycleCutoverPostgresRow(t, context.Background(), pool, lifecycleCutoverObservation{orgID: orgID, sessionID: sessionID, state: models.LifecycleCutoverStateShadow})
			}
			before := lifecycleProtocolRows(t, context.Background(), pool, orgID, sessionID)
			migrations := []string{
				"000348_cutover_protocol_retention.down.sql", "000347_session_protocol_retention.down.sql", "000346_lifecycle_protocol_retention_functions.down.sql",
				"000346_lifecycle_protocol_retention_functions.up.sql", "000347_session_protocol_retention.up.sql", "000348_cutover_protocol_retention.up.sql",
			}
			for _, name := range migrations {
				migration, err := os.ReadFile("../../migrations/" + name)
				require.NoError(t, err, "read each shipped migration in reverse then forward dependency order")
				tx := beginSessionLifecycleProbe(t, context.Background(), pool)
				_, err = tx.Exec(context.Background(), string(migration))
				require.NoError(t, err, "inactive rollback and reinstallation must succeed in separate bounded migration transactions: %s", name)
				require.NoError(t, tx.Commit(context.Background()), "commit each actual migration transaction before applying its successor: %s", name)
			}
			require.Equal(t, before, lifecycleProtocolRows(t, context.Background(), pool, orgID, sessionID), "an inactive down/up round trip must preserve every retained legacy field")
			_, err := pool.Exec(context.Background(), `TRUNCATE session_lifecycle_cutovers`)
			require.NoError(t, err, "uncontended never-activated cutover maintenance must remain possible after guard reinstallation")
			_, err = pool.Exec(context.Background(), `UPDATE sessions SET lifecycle_version=1 WHERE org_id=$1 AND id=$2`, orgID, sessionID)
			require.NoError(t, err, "positive-only synthetic evidence remains representable without claiming valid activation")
			_, err = pool.Exec(context.Background(), `UPDATE sessions SET lifecycle_version=0 WHERE org_id=$1 AND id=$2`, orgID, sessionID)
			requireSessionLifecycleSQLState(t, err, "55000", "the reinstalled parent guard must retain positive-only protocol evidence")
			_, err = pool.Exec(context.Background(), `TRUNCATE session_lifecycle_cutovers`)
			requireSessionLifecycleSQLState(t, err, "55000", "the reinstalled truncate guard must retain positive parent evidence even without a cutover")
		})
	}
}

func TestLifecycleProtocolRollbackSeesConcurrentActivationPostgres(t *testing.T) {
	t.Parallel()
	source := newLifecycleScopeSnapshotPool(t)
	template := seedActivatedLifecycleFixture(t, context.Background(), source)
	tests := []struct{ name, migration string }{
		{name: "parent guard rollback", migration: "000347_session_protocol_retention.down.sql"},
		{name: "cutover guard rollback", migration: "000348_cutover_protocol_retention.down.sql"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			orgID, sessionID, _ := seedRuntimeSeedThread(t, pool)
			migration, err := os.ReadFile("../../migrations/" + tt.migration)
			require.NoError(t, err, "execute the exact shipped rollback whose preflight must serialize with activation")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			activationCtx, stopActivation := context.WithTimeout(ctx, LifecycleTransactionTimeout)
			defer stopActivation()
			activator := beginSessionLifecycleProbe(t, activationCtx, pool)
			require.NoError(t, ConfigureLifecycleTransactionInTx(activationCtx, activator), "configure the independent synthetic metadata writer")
			require.NoError(t, tryLockLifecycleCutoverExclusiveInTx(activationCtx, activator, orgID, sessionID), "hold the exact session interlock before staging activation")
			_, err = activator.Exec(activationCtx, `UPDATE sessions SET lifecycle_version=1 WHERE org_id=$1 AND id=$2`, orgID, sessionID)
			require.NoError(t, err, "stage the positive parent while retaining its table write lock until commit")
			_, err = activator.Exec(activationCtx, `UPDATE session_threads SET lifecycle_version=1 WHERE org_id=$1 AND session_id=$2`, orgID, sessionID)
			require.NoError(t, err, "stage the complete visible lane cohort in the same synthetic activation")
			insertLifecycleCutoverPostgresRow(t, activationCtx, activator, lifecycleProtocolCutoverRow(t, template.cutover, orgID, sessionID, "active", 3))
			rollback := beginSessionLifecycleProbe(t, ctx, pool)
			rollbackPID := backendPID(t, ctx, rollback)
			result, finished := make(chan error, 1), make(chan struct{})
			go func() {
				defer close(finished)
				_, rollbackErr := rollback.Exec(ctx, string(migration))
				result <- rollbackErr
			}()
			t.Cleanup(func() {
				cancel()
				cleanupCtx, stop := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
				defer stop()
				waitForResult(t, cleanupCtx, finished)
			})
			waitForBlockedBackend(t, activationCtx, activator, rollbackPID)
			require.NoError(t, activator.Commit(activationCtx), "commit activation only after the rollback is physically waiting on its retained relation lock")
			err = waitForResult(t, ctx, result)
			waitForResult(t, ctx, finished)
			requireSessionLifecycleSQLState(t, err, "55000", "rollback must freshly discover the activation committed while it waited, before removing either guard")
			require.ErrorContains(t, err, "LIFECYCLE_GUARD_PROVENANCE_REQUIRED", "a dependency error is not proof that the rollback preflight observed activation")
			require.NoError(t, rollback.Rollback(ctx), "discard the refused migration transaction without removing any guard")
			_, err = pool.Exec(ctx, `UPDATE sessions SET lifecycle_version=0 WHERE org_id=$1 AND id=$2`, orgID, sessionID)
			requireSessionLifecycleSQLState(t, err, "55000", "the parent retention guard must remain effective after concurrent rollback refusal")
			_, err = pool.Exec(ctx, `DELETE FROM session_lifecycle_cutovers WHERE org_id=$1 AND session_id=$2`, orgID, sessionID)
			requireSessionLifecycleSQLState(t, err, "55000", "the cutover retention guard must remain effective after concurrent rollback refusal")
		})
	}
}

func TestLifecycleProtocolTruncateSerializesParentEvidencePostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleScopeSnapshotPool(t)
	orgID, sessionID, _ := seedRuntimeSeedThread(t, pool)
	insertLifecycleCutoverPostgresRow(t, context.Background(), pool, lifecycleCutoverObservation{orgID: orgID, sessionID: sessionID, state: models.LifecycleCutoverStateShadow})
	ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
	defer cancel()
	writer := beginSessionLifecycleProbe(t, ctx, pool)
	require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, writer), "configure the independent positive-evidence writer")
	_, err := writer.Exec(ctx, `UPDATE sessions SET lifecycle_version=1 WHERE org_id=$1 AND id=$2`, orgID, sessionID)
	require.NoError(t, err, "stage retained positive evidence without manufacturing a complete active cutover")
	actor := beginSessionLifecycleProbe(t, ctx, pool)
	_, err = actor.Exec(ctx, `TRUNCATE session_lifecycle_cutovers`)
	requireSessionLifecycleSQLState(t, err, "55P03", "TRUNCATE must nonwaitingly exclude an in-flight parent writer before checking retained evidence")
	require.NotContains(t, err.Error(), "LIFECYCLE_CUTOVER_INTERLOCK_BUSY", "a relation-lock conflict must not manufacture the retryable advisory-interlock cause")
	require.NoError(t, actor.Rollback(ctx), "discard the whole refused truncation before committing positive parent evidence")
	before := lifecycleProtocolRows(t, ctx, writer, orgID, sessionID)
	require.NoError(t, writer.Commit(ctx), "the writer's independently retained positive evidence must still commit")
	require.Equal(t, before, lifecycleProtocolRows(t, ctx, pool, orgID, sessionID), "TRUNCATE refusal must preserve the shadow cutover and every exact positive parent and lane field")
}

func TestLifecycleProtocolBulkRequiresReadCommittedPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, query string
		isolation   pgx.TxIsoLevel
	}{
		{name: "repeatable read truncate", query: `TRUNCATE session_lifecycle_cutovers`, isolation: pgx.RepeatableRead},
		{name: "serializable truncate", query: `TRUNCATE session_lifecycle_cutovers`, isolation: pgx.Serializable},
		{name: "repeatable read rollback", query: "000348_cutover_protocol_retention.down.sql", isolation: pgx.RepeatableRead},
		{name: "serializable rollback", query: "000348_cutover_protocol_retention.down.sql", isolation: pgx.Serializable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			actor, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: tt.isolation})
			require.NoError(t, err, "begin the incompatible bulk transaction before independent activation")
			defer func() { _ = actor.Rollback(context.Background()) }()
			var count int
			require.NoError(t, actor.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&count), "establish the old transaction snapshot while this isolated database has no sessions")
			require.Equal(t, 0, count, "the bulk actor must actually predate all retained activation evidence")
			fixture := seedActivatedLifecycleFixture(t, ctx, pool)
			before := lifecycleProtocolRows(t, ctx, pool, fixture.orgID, fixture.sessionID)
			query := tt.query
			if strings.HasSuffix(query, ".sql") {
				migration, readErr := os.ReadFile("../../migrations/" + query)
				require.NoError(t, readErr, "exercise the shipped rollback rather than a duplicated assertion")
				query = string(migration)
			}
			_, err = actor.Exec(ctx, query)
			requireSessionLifecycleSQLState(t, err, "55000", "bulk provenance checks must reject an incompatible snapshot isolation level before dropping evidence or guards")
			require.ErrorContains(t, err, "LIFECYCLE_READ_COMMITTED_REQUIRED", "the refusal must identify isolation, not claim that the stale snapshot observed active state")
			require.NoError(t, actor.Rollback(ctx), "discard all bulk DDL and its old snapshot after the fail-closed refusal")
			require.Equal(t, before, lifecycleProtocolRows(t, ctx, pool, fixture.orgID, fixture.sessionID), "isolation refusal must retain the complete committed activation evidence")
			_, err = pool.Exec(ctx, `DELETE FROM session_lifecycle_cutovers WHERE org_id=$1 AND session_id=$2`, fixture.orgID, fixture.sessionID)
			requireSessionLifecycleSQLState(t, err, "55000", "the cutover guard must still be installed after bulk isolation refusal")
		})
	}
}

func lifecycleProtocolCutoverRow(t *testing.T, template lifecycleCutoverObservation, orgID, sessionID uuid.UUID, state string, revision int64) lifecycleCutoverObservation {
	t.Helper()
	row := template
	row.orgID, row.sessionID, row.version = orgID, sessionID, revision
	row.state = models.LifecycleCutoverState(state)
	if state != "active" {
		row.activatedAt, row.minimumWriterVersion, row.resumabilityCapability = nil, nil, nil
		row.normalization = lifecycleProtocolJSONField(t, row.normalization, "complete", false)
	}
	if state == "shadow" {
		row.normalization, row.legacyInputCutoffID = nil, nil
	}
	return row
}

func lifecycleProtocolJSONField(t *testing.T, original json.RawMessage, field string, value any) json.RawMessage {
	t.Helper()
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(original, &envelope), "copy only the selected frozen synthetic envelope before modifying a negative fixture")
	encoded, err := json.Marshal(value)
	require.NoError(t, err, "encode the selected synthetic envelope boundary value")
	envelope[field] = encoded
	result, err := json.Marshal(envelope)
	require.NoError(t, err, "retain valid JSON while making the exact selected provenance shape invalid")
	return result
}

func lifecycleProtocolRows(t *testing.T, ctx context.Context, database DBTX, orgID, sessionID uuid.UUID) json.RawMessage {
	t.Helper()
	var result json.RawMessage
	err := database.QueryRow(ctx, `SELECT jsonb_build_object('session',(SELECT to_jsonb(s) FROM sessions s WHERE s.org_id=$1 AND s.id=$2),'cutover',(SELECT to_jsonb(c) FROM session_lifecycle_cutovers c WHERE c.org_id=$1 AND c.session_id=$2),'threads',(SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t.id),'[]'::jsonb) FROM session_threads t WHERE t.org_id=$1 AND t.session_id=$2))`, orgID, sessionID).Scan(&result)
	require.NoError(t, err, "capture all scoped parent cutover and retained lane fields independently of the resolver")
	return result
}

type lifecycleProtocolObservationTx struct {
	pgx.Tx
	failure            string
	observationQueries int
	afterScan          context.CancelFunc
}

func (tx *lifecycleProtocolObservationTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if query != lifecycleModeObservationQuery {
		return tx.Tx.QueryRow(ctx, query, args...)
	}
	tx.observationQueries++
	if tx.failure == "query" {
		return tx.Tx.QueryRow(ctx, `SELECT synthetic_missing_protocol_column FROM sessions WHERE org_id=$1 AND id=$2`, args...)
	}
	if tx.failure == "scan" {
		return tx.Tx.QueryRow(ctx, `SELECT $1::uuid,$2::uuid,1::smallint,$1::uuid,$2::uuid,'invalid-revision'::text,'active'::text,1::bigint,NULL::jsonb,NULL::timestamptz,1::integer,NULL::jsonb,'codex'::text,'[{"protocol_version":1,"provider":"codex"}]'::jsonb`, args...)
	}
	return lifecycleProtocolObservationRow{Row: tx.Tx.QueryRow(ctx, query, args...), afterScan: tx.afterScan}
}

type lifecycleProtocolObservationRow struct {
	pgx.Row
	afterScan context.CancelFunc
}

func (row lifecycleProtocolObservationRow) Scan(dest ...any) error {
	err := row.Row.Scan(dest...)
	if err == nil && row.afterScan != nil {
		row.afterScan()
	}
	return err
}
