package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func retainedReviewGateFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, source models.ReviewLoopSource) (callerSendSchemaFixture, RetainedReviewGateScope, models.SessionPublication) {
	t.Helper()
	_, err := pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
	require.NoError(t, err, "enable retained SQL fixtures only in the disposable database")
	tx := callerSendSchemaBegin(t, ctx, pool)
	f := callerSendSchemaSeed(t, ctx, tx)
	_, publicationID, changeset := callerSendSchemaCaller(t, ctx, tx, f, "publication")
	scope := RetainedReviewGateScope{SessionID: f.session, ChangesetID: changeset, StartID: f.owner, Source: source}
	caller := changeset
	if source == models.ReviewLoopSourceAutomation {
		_, runID, _ := callerSendSchemaCaller(t, ctx, tx, f, "automation")
		scope.AutomationRunID, caller = &runID, runID
	}
	require.NoError(t, callerSendSchemaInsertMap(ctx, tx, "review_loop_start_intents", callerSendSchemaStartRow(f, string(source), caller)), "retain a legal original start with exact caller scope before any gate mapping")
	if source == models.ReviewLoopSourcePublication {
		_, err = tx.Exec(ctx, `UPDATE session_publications SET review_start_id=$3 WHERE org_id=$1 AND id=$2`, f.org, publicationID, f.owner)
	} else {
		_, err = tx.Exec(ctx, `UPDATE automation_runs SET review_start_id=$3 WHERE org_id=$1 AND id=$2`, f.org, scope.AutomationRunID, f.owner)
	}
	require.NoError(t, err, "bind the caller's actual retained generation")
	_, err = tx.Exec(ctx, `UPDATE session_publications SET state='review_pending',review_gate_state='pending',review_max_passes=2 WHERE org_id=$1 AND id=$2`, f.org, publicationID)
	require.NoError(t, err, "begin with a genuine pending publication gate")
	require.NoError(t, tx.Commit(ctx), "commit the independently retained caller before later actions and gate writes")
	publication, err := NewSessionPublicationStore(pool).GetByChangeset(ctx, f.org, f.session, changeset)
	require.NoError(t, err, "load the original pending publication candidate before a possible bypass or replacement")
	return f, scope, publication
}

func retainedReviewGateGraph(t *testing.T, ctx context.Context, pool *pgxpool.Pool, orgID uuid.UUID) json.RawMessage {
	t.Helper()
	var graph json.RawMessage
	err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
 'publications',(SELECT COALESCE(jsonb_agg(to_jsonb(p) ORDER BY p.id),'[]'::jsonb) FROM session_publications p WHERE p.org_id=$1),
 'starts',(SELECT COALESCE(jsonb_agg(to_jsonb(s) ORDER BY s.id),'[]'::jsonb) FROM review_loop_start_intents s WHERE s.org_id=$1),
 'runs',(SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY a.id),'[]'::jsonb) FROM automation_runs a WHERE a.org_id=$1))`, orgID).Scan(&graph)
	require.NoError(t, err, "snapshot every publication, retained start and automation caller field privately")
	return graph
}

func TestRetainedPublicationReviewGateCASPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, scenario string
		source         models.ReviewLoopSource
		gate           models.SessionPublicationReviewGateState
		invalidate     bool
	}{
		{name: "publication pending same generation fails finitely", source: models.ReviewLoopSourcePublication, gate: models.SessionPublicationReviewGateFailed},
		{name: "publication pending same generation needs human", source: models.ReviewLoopSourcePublication, gate: models.SessionPublicationReviewGateNeedsHuman},
		{name: "publication stale pending after committed bypass", source: models.ReviewLoopSourcePublication, gate: models.SessionPublicationReviewGateNeedsHuman, scenario: "bypass"},
		{name: "publication stale generation", source: models.ReviewLoopSourcePublication, gate: models.SessionPublicationReviewGateFailed, scenario: "generation"},
		{name: "publication passed target is not reopened", source: models.ReviewLoopSourcePublication, gate: models.SessionPublicationReviewGateNeedsHuman, scenario: "passed"},
		{name: "publication terminal target remains terminal", source: models.ReviewLoopSourcePublication, gate: models.SessionPublicationReviewGateNeedsHuman, scenario: "terminal"},
		{name: "publication foreign tenant", source: models.ReviewLoopSourcePublication, gate: models.SessionPublicationReviewGateFailed, scenario: "foreign"},
		{name: "automation pending same generation fails finitely", source: models.ReviewLoopSourceAutomation, gate: models.SessionPublicationReviewGateFailed},
		{name: "automation stale pending after committed bypass", source: models.ReviewLoopSourceAutomation, gate: models.SessionPublicationReviewGatePassed, scenario: "bypass"},
		{name: "automation stale generation", source: models.ReviewLoopSourceAutomation, gate: models.SessionPublicationReviewGateFailed, scenario: "generation"},
		{name: "automation passed target is not reopened", source: models.ReviewLoopSourceAutomation, gate: models.SessionPublicationReviewGatePending, scenario: "passed"},
		{name: "automation terminal target remains terminal", source: models.ReviewLoopSourceAutomation, gate: models.SessionPublicationReviewGatePassed, scenario: "terminal"},
		{name: "automation foreign tenant", source: models.ReviewLoopSourceAutomation, gate: models.SessionPublicationReviewGateFailed, scenario: "foreign"},
		{name: "automation clean repeat matches original passed owner", source: models.ReviewLoopSourceAutomation, gate: models.SessionPublicationReviewGatePassed, scenario: "repeat"},
		{name: "automation failed repeat matches original terminal owner", source: models.ReviewLoopSourceAutomation, gate: models.SessionPublicationReviewGateFailed, scenario: "repeat"},
		{name: "automation needs human repeat matches original blocked owner", source: models.ReviewLoopSourceAutomation, gate: models.SessionPublicationReviewGateNeedsHuman, scenario: "repeat"},
		{name: "publication failed repeat matches original terminal owner", source: models.ReviewLoopSourcePublication, gate: models.SessionPublicationReviewGateFailed, scenario: "repeat"},
		{name: "publication needs human repeat matches original blocked owner", source: models.ReviewLoopSourcePublication, gate: models.SessionPublicationReviewGateNeedsHuman, scenario: "repeat"},
		{name: "retained passed evidence invalidates same generation", source: models.ReviewLoopSourcePublication, invalidate: true},
		{name: "retained invalidation cannot undo bypass", source: models.ReviewLoopSourcePublication, invalidate: true, scenario: "bypass"},
		{name: "retained invalidation cannot touch replacement generation", source: models.ReviewLoopSourcePublication, invalidate: true, scenario: "generation"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../migrations", 2)
			f, scope, loaded := retainedReviewGateFixture(t, ctx, pool, tt.source)
			store := NewSessionPublicationStore(pool)
			if tt.invalidate || tt.scenario == "passed" {
				require.NoError(t, store.SetReviewGate(ctx, f.org, f.session, scope.ChangesetID, models.SessionPublicationReviewGatePassed), "establish the existing legal passed-evidence state before invalidation or stale writes")
			}
			switch tt.scenario {
			case "bypass":
				bypass := loaded
				bypass.Source = models.SessionPublicationSourceUser
				bypass.TriggerKind = models.SessionPublicationTriggerExplicitAction
				bypass.AutomaticPolicySource = models.PublicationPolicySourceExplicitAction
				bypass.ReviewPolicySource = models.PublicationPolicySourceExplicitBypass
				bypass.RequestPayload = json.RawMessage(`{"synthetic":true}`)
				require.NoError(t, store.ApplyReviewBypass(ctx, f.org, &bypass), "commit the real explicit bypass after loading the old pending candidate")
			case "generation":
				tx := callerSendSchemaBegin(t, ctx, pool)
				_, err := tx.Exec(ctx, `UPDATE review_loop_start_intents SET state='refused',due_at=NULL,detail='running_conflict' WHERE org_id=$1 AND id=$2`, f.org, f.owner)
				require.NoError(t, err, "positively resolve only the old preparation before a legal caller generation replacement")
				fresh := f
				fresh.owner, fresh.pass = uuid.New(), uuid.New()
				caller := scope.ChangesetID
				if tt.source == models.ReviewLoopSourceAutomation {
					caller = *scope.AutomationRunID
				}
				require.NoError(t, callerSendSchemaInsertMap(ctx, tx, "review_loop_start_intents", callerSendSchemaStartRow(fresh, string(tt.source), caller)), "retain a genuine different same-caller start generation")
				if tt.source == models.ReviewLoopSourcePublication {
					_, err = tx.Exec(ctx, `UPDATE session_publications SET review_start_id=$4 WHERE org_id=$1 AND session_id=$2 AND changeset_id=$3`, f.org, f.session, scope.ChangesetID, fresh.owner)
				} else {
					_, err = tx.Exec(ctx, `UPDATE automation_runs SET review_start_id=$3 WHERE org_id=$1 AND id=$2`, f.org, scope.AutomationRunID, fresh.owner)
				}
				require.NoError(t, err, "make the newer generation the actual current caller without bypassing any trigger")
				require.NoError(t, tx.Commit(ctx), "commit replacement generation before applying the stale candidate")
			case "terminal":
				require.NoError(t, store.SetReviewGate(ctx, f.org, f.session, scope.ChangesetID, models.SessionPublicationReviewGateFailed), "commit a finite existing terminal outcome before the stale mapping")
			}
			before := retainedReviewGateGraph(t, ctx, pool, f.org)
			requestOrg := f.org
			if tt.scenario == "foreign" {
				requestOrg = uuid.New()
			}
			var matched bool
			var err error
			if tt.invalidate {
				matched, err = store.InvalidateRetainedReviewEvidence(ctx, requestOrg, scope, "synthetic-new-head")
			} else {
				matched, err = store.SetRetainedReviewGate(ctx, requestOrg, scope, tt.gate)
			}
			require.NoError(t, err, "stale or bypassed retained outcomes must be finite scoped no-ops")
			expectedMatch := tt.scenario == "" || tt.scenario == "repeat"
			require.Equal(t, expectedMatch, matched, "only the exact current expected gate or idempotent same finite outcome may match")
			after := retainedReviewGateGraph(t, ctx, pool, f.org)
			if !expectedMatch {
				require.Equal(t, before, after, "lost scope/generation/gate CAS must preserve every newer caller, retained start and publication field exactly")
			} else {
				actual, err := store.GetByChangeset(ctx, f.org, f.session, scope.ChangesetID)
				require.NoError(t, err, "read the actual matched current publication")
				expectedGate := tt.gate
				if tt.invalidate {
					expectedGate = models.SessionPublicationReviewGatePending
					require.Nil(t, actual.ReviewLoopID, "retained invalidation preserves the existing link-clearing behavior")
					require.Nil(t, actual.ReviewWorkspaceRevision, "retained invalidation clears stale revision evidence")
					require.Nil(t, actual.ReviewDesiredHeadSHA, "retained invalidation clears stale reviewed head evidence")
					head := "synthetic-new-head"
					require.Equal(t, &head, actual.DesiredHeadSHA, "retained invalidation keeps the exact existing replacement-head behavior")
				}
				require.Equal(t, expectedGate, actual.ReviewGateState, "known current owner receives the established review-gate outcome")
				require.Equal(t, loaded.ReviewStartID, actual.ReviewStartID, "successful mapping never clears or replaces private caller identity")
			}
			if tt.scenario == "repeat" {
				repeatBefore := retainedReviewGateGraph(t, ctx, pool, f.org)
				matched, err := store.SetRetainedReviewGate(ctx, f.org, scope, tt.gate)
				require.NoError(t, err, "ordinary replay of the same committed finite outcome must stay usable")
				require.True(t, matched, "same current committed finite result retains its existing outcome semantics")
				require.Equal(t, repeatBefore, retainedReviewGateGraph(t, ctx, pool, f.org), "idempotent finite-outcome match cannot rewrite evidence, timestamps or any caller field")
			}
		})
	}
}
