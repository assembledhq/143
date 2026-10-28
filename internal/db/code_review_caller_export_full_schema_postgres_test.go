package db

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// This uses the complete migration chain and real retained-owner transitions.
// Its command JSON/digests are SQL-shape fixtures, not full codec envelopes or
// evidence of send acceptance. It tests retained-store privacy, not HTTP output.
//
//nolint:paralleltest // Subcases share a one-connection pool and database-wide enablement.
func TestCodeReviewExportPostgresCallerIntentFullSchemaNonDisclosure(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool := testpostgres.NewPool(t, ctx, "../../migrations", 1)
	_, err := pool.Exec(ctx, `SET TIME ZONE 'UTC'`)
	require.NoError(t, err, "use an explicit UTC presentation on this isolated one-connection pool")
	var ready, enabled bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT schema_ready,intents_enabled FROM caller_send_intent_control WHERE singleton`).Scan(&ready, &enabled), "the complete schema must expose its explicit caller-intent interlock")
	require.Equal(t, [2]bool{true, false}, [2]bool{ready, enabled}, "migration readiness must not silently enable caller intents")
	_, err = pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready`)
	require.NoError(t, err, "enable retained fixtures only in this disposable database")

	tests := []struct {
		name string
		kind models.ReviewExportSourceKind
	}{
		{name: "legacy membership", kind: models.ReviewExportLegacy},
		{name: "assessment membership", kind: models.ReviewExportAssessment},
	}
	for _, tt := range tests {
		// Cases share a database-wide enablement flag and the explicitly bounded
		// one-connection pool, so they run sequentially with fresh tenant scopes.
		t.Run(tt.name, func(t *testing.T) {
			tx := callerSendSchemaBegin(t, ctx, pool)
			f := callerSendSchemaSeed(t, ctx, tx)
			f.at = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
			integrationID, repositoryID, pullRequestID, policyID, metadataID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			_, err = tx.Exec(ctx, `INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')`, integrationID, f.org)
			require.NoError(t, err, "create a synthetic integration through the complete schema")
			_, err = tx.Exec(ctx, `INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id)
VALUES($1,$2,$3,(hashtextextended($1::uuid::text,0)&9223372036854775807),'synthetic/export-privacy','https://example.invalid/export-privacy.git',1)`, repositoryID, f.org, integrationID)
			require.NoError(t, err, "retain a real owned repository without weakening its constraints")
			_, err = tx.Exec(ctx, `INSERT INTO pull_requests(id,org_id,session_id,github_pr_number,github_pr_url,github_repo,title,status,head_sha)
VALUES($1,$2,$3,7,'https://example.invalid/pull/7','synthetic/export-privacy','Synthetic retained export','open','synthetic-head')`, pullRequestID, f.org, f.session)
			require.NoError(t, err, "retain the exact owned pull request required by assessment capture")
			_, err = tx.Exec(ctx, `INSERT INTO code_review_policies(id,org_id,version,approval_mode,review_instructions,automated_approval_policy,description_policy,risk_policy,agent_roster)
VALUES($1,$2,1,'comment_only','Synthetic public review instructions','Synthetic public approval policy','{}','{"max_additions":750}','{}')`, policyID, f.org)
			require.NoError(t, err, "retain a valid versioned public policy snapshot")
			_, err = tx.Exec(ctx, `INSERT INTO code_review_session_metadata(id,org_id,session_id,repository_id,pull_request_id,policy_id,base_sha,head_sha,trigger_source,status,review_output_key,github_review_id,created_at,completed_at)
VALUES($1,$2,$3,$4,$5,$6,'synthetic-base','synthetic-head','slash_command','completed','synthetic-public-output',101,'2026-08-01','2026-08-01')`, metadataID, f.org, f.session, repositoryID, pullRequestID, policyID)
			require.NoError(t, err, "seed one exact historical source rather than an empty agent-results join")

			sourceID := metadataID
			var assessmentID *uuid.UUID
			var publicAssessment any
			if tt.kind == models.ReviewExportAssessment {
				capture := testAssessmentCapture()
				capture.OrgID, capture.RepositoryID, capture.PullRequestID, capture.PolicyID, capture.SessionID = f.org, repositoryID, pullRequestID, policyID, f.session
				capture.PublicationKey = "synthetic-public-output"
				assessment, reused, createErr := NewCodeReviewAssessmentStore(tx).Create(ctx, capture)
				require.NoError(t, createErr, "capture the assessment through the real scoped store before assigning its reviewers")
				require.False(t, reused, "the isolated assessment fixture must insert its own identity")
				_, err = tx.Exec(ctx, `UPDATE code_review_revision_assessments SET created_at='2026-08-01' WHERE org_id=$1 AND id=$2`, f.org, assessment.ID)
				require.NoError(t, err, "place the real assessment inside the explicit historical acquisition interval")
				sourceID, assessmentID, publicAssessment = assessment.ID, &assessment.ID, assessment.ID.String()
			}

			attention, accepted, loop := f, f, f
			attention.owner, accepted.owner, loop.owner, loop.pass = uuid.New(), uuid.New(), uuid.New(), uuid.New()
			markers := []string{
				"synthetic-private-dispatch-attention-input", "synthetic-private-dispatch-attention-wake", "synthetic-private-dispatch-attention-raw",
				"synthetic-private-dispatch-accepted-input", "synthetic-private-dispatch-accepted-wake", "synthetic-private-dispatch-accepted-raw",
				"synthetic-private-startup-input", "synthetic-private-start-envelope",
			}
			seedCallerExportReviewer(t, ctx, tx, attention, assessmentID, "attention", "2026-08-02T00:00:00Z", strings.Repeat("c", 64), markers[0:3])
			seedCallerExportReviewer(t, ctx, tx, accepted, assessmentID, "observed-manual", "2026-08-03T00:00:00Z", strings.Repeat("d", 64), markers[3:6])

			startupOwner := callerSendSchemaOwners()[1]
			startup := callerSendSchemaShape(startupOwner, loop, "pending")
			startup["input"] = markedCallerExportJSON(t, startup["input"].(string), markers[6])
			startup["digest"] = strings.Repeat("e", 64)
			start := callerSendSchemaStartRow(loop, "manual", uuid.Nil)
			start["startup_input"], start["startup_digest"] = startup["input"], startup["digest"]
			start["envelope"], start["envelope_digest"] = markedCallerExportJSON(t, `{}`, markers[7]), strings.Repeat("f", 64)
			require.NoError(t, callerSendSchemaInsertMap(ctx, tx, "review_loop_start_intents", start), "install a privately marked prepared creation envelope before its loop exists")
			require.NoError(t, callerSendSchemaInsertOwner(ctx, tx, startupOwner, loop, startup), "create the exact start, startup owner and first pass with real deferred binding checks")
			_, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
			require.NoError(t, err, "flush every actual retained scope and creation constraint before committing the seed")
			require.NoError(t, tx.Commit(ctx), "commit all legal retained owners before pool-backed Acquire establishes its own repeatable-read transaction")

			privateDispatchFields := []string{
				"dispatch_version", "dispatch_key", "dispatch_input", "dispatch_digest", "dispatch_thread_id", "dispatch_state", "dispatch_attempt_id", "dispatch_attempt_seq", "dispatch_attempted_at", "dispatch_due_at", "dispatch_observe_deadline", "dispatch_attention_at", "dispatch_observed_at", "dispatch_message_id", "dispatch_resolution", "dispatch_detail", "dispatch_writer_seq", "dispatch_updated_at", "dispatch_wake",
			}
			var populated []string
			err = pool.QueryRow(ctx, `SELECT array_agg(field ORDER BY field) FROM unnest($3::text[]) AS fields(field)
WHERE EXISTS (SELECT 1 FROM code_review_agent_results ar WHERE ar.org_id=$1 AND ar.session_id=$2
AND to_jsonb(ar)->field IS NOT NULL AND to_jsonb(ar)->field <> 'null'::jsonb)`, f.org, f.session, privateDispatchFields).Scan(&populated)
			require.NoError(t, err, "positively inspect private reviewer values committed through real constraints")
			expectedPopulated := append([]string(nil), privateDispatchFields...)
			sort.Strings(expectedPopulated)
			require.Equal(t, expectedPopulated, populated, "legal attention and observed acceptance together must populate every private reviewer field without an impossible combined state")
			var retainedStates []string
			require.NoError(t, pool.QueryRow(ctx, `SELECT array_agg(dispatch_state ORDER BY created_at,id) FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2`, f.org, f.session).Scan(&retainedStates), "read the actual committed legal reviewer states rather than trusting the transition helper")
			require.Equal(t, []string{"attention", "accepted"}, retainedStates, "the two legal rows must exercise unresolved and observed-accepted storage simultaneously")
			var counts [4]int
			require.NoError(t, pool.QueryRow(ctx, `SELECT
(SELECT count(*) FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2),
(SELECT count(*) FROM session_review_loops WHERE org_id=$1 AND session_id=$2),
(SELECT count(*) FROM review_loop_start_intents WHERE org_id=$1 AND session_id=$2 AND state='created'),
(SELECT count(*) FROM session_review_loop_passes WHERE org_id=$1 AND session_id=$2)`, f.org, f.session).Scan(&counts[0], &counts[1], &counts[2], &counts[3]), "verify committed reviewer and creation units exist before export selection")
			require.Equal(t, [4]int{2, 1, 1, 1}, counts, "the privacy test must not pass on absent retained fixtures")
			var privateRows json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT jsonb_build_object(
'reviewers',(SELECT jsonb_agg(to_jsonb(ar)) FROM code_review_agent_results ar WHERE ar.org_id=$1 AND ar.session_id=$2),
'startup',(SELECT to_jsonb(l) FROM session_review_loops l WHERE l.org_id=$1 AND l.session_id=$2 AND l.id=$3),
'creation',(SELECT to_jsonb(s) FROM review_loop_start_intents s WHERE s.org_id=$1 AND s.session_id=$2 AND s.id=$3))`, f.org, f.session, loop.owner).Scan(&privateRows), "read actual committed private values rather than trusting only the fixture builders")
			privateValues := append(append([]string(nil), markers...), "cr-dispatch:v1:"+attention.owner.String(), "cr-dispatch:v1:"+accepted.owner.String(), "rl-startup:v1:"+loop.owner.String(), strings.Repeat("c", 64), strings.Repeat("d", 64), strings.Repeat("e", 64), strings.Repeat("f", 64))
			for _, value := range privateValues {
				require.Contains(t, string(privateRows), value, "each exact private-only value must really be present in retained source rows")
			}

			filters := models.CodeReviewExportFilters{RepositoryID: repositoryID, From: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC), TimeBasis: models.ReviewExportCreated, IncludeUnresolvedPublicationTime: true}
			store := NewCodeReviewExportStore(pool)
			acquisition, err := store.Acquire(ctx, f.org, filters)
			require.NoError(t, err, "the actual pool-backed acquisition must materialize the committed full-schema source")
			require.Equal(t, int64(1), acquisition.Counts.Records, "each source-kind branch must select its one intended source")
			pageAcquisition, rows, err := store.Page(ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: acquisition.ID}, 100)
			require.NoError(t, err, "read actual retained payloads rather than marshal a live reviewer model")
			require.Equal(t, acquisition, pageAcquisition, "paging must retain this exact acquisition boundary")
			require.Len(t, rows, 1, "the source identity assertion requires its single actual retained object")
			require.Equal(t, sourceID, rows[0].ID, "the selected source must match this branch's independently retained identity")
			require.Equal(t, tt.kind, rows[0].Kind, "both legacy and assessment agent-result membership branches must actually execute")
			var actualAgentResults []map[string]any
			require.NoError(t, json.Unmarshal(rows[0].AgentResults, &actualAgentResults), "decode the actual SQL-projected reviewer objects")
			expectedAgentResults := []map[string]any{
				{
					"id": attention.owner.String(), "session_id": f.session.String(), "assessment_id": publicAssessment,
					"agent_provider": "codex", "agent_model": "synthetic-export-model", "role": "reviewer", "status": "queued",
					"structured_result": map[string]any{"thread_id": f.thread.String(), "summary": "Synthetic public reviewer output", "state": "visible", "digest": "public-evidence-digest"}, "created_at": "2026-08-02T00:00:00+00:00",
				},
				{
					"id": accepted.owner.String(), "session_id": f.session.String(), "assessment_id": publicAssessment,
					"agent_provider": "codex", "agent_model": "synthetic-export-model", "role": "reviewer", "status": "queued",
					"structured_result": map[string]any{"thread_id": f.thread.String(), "summary": "Synthetic public reviewer output", "state": "visible", "digest": "public-evidence-digest"}, "created_at": "2026-08-03T00:00:00+00:00",
				},
			}
			require.Equal(t, expectedAgentResults, actualAgentResults, "the independent complete nine-key public allowlist must survive exactly, including deliberate public thread identity and generic state/digest evidence")

			var payload json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM code_review_export_rows WHERE org_id=$1 AND export_id=$2 AND position=1`, f.org, acquisition.ID).Scan(&payload), "inspect the actual frozen database payload, not just its model projection")
			var persisted map[string]any
			require.NoError(t, json.Unmarshal(payload, &persisted), "decode the complete persisted export source")
			keys := make([]string, 0, len(persisted))
			for key := range persisted {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			require.Equal(t, []string{"agent_results", "id", "kind", "legacy_aliases", "policy", "provider_observations", "publication_time", "publication_time_provenance", "pull_request", "source"}, keys, "the frozen source must not introduce any private startup, start-intent or stamp object boundary")
			require.Equal(t, tt.kind, models.ReviewExportSourceKind(persisted["kind"].(string)), "the persisted source kind must be the same selected branch")
			require.Equal(t, sourceID.String(), persisted["id"], "the persisted source identity must be the exact selected owner")
			persistedAgentResults, encodeErr := json.Marshal(persisted["agent_results"])
			require.NoError(t, encodeErr, "encode only the actual frozen reviewer aggregate for the independent allowlist comparison")
			var decodedPersistedAgentResults []map[string]any
			require.NoError(t, json.Unmarshal(persistedAgentResults, &decodedPersistedAgentResults), "decode the persisted aggregate without deriving its expected keys from the exclusion list")
			require.Equal(t, expectedAgentResults, decodedPersistedAgentResults, "the full persisted payload must contain exactly the same scrubbed public reviewer objects")
			privateFieldNames := append([]string(nil), privateDispatchFields...)
			privateFieldNames = append(privateFieldNames,
				"startup_send_version", "startup_send_key", "startup_send_input", "startup_send_digest", "startup_send_thread_id", "startup_send_state", "startup_send_attempt_id", "startup_send_attempt_seq", "startup_send_attempted_at", "startup_send_due_at", "startup_send_observe_deadline", "startup_send_attention_at", "startup_send_observed_at", "startup_send_message_id", "startup_send_resolution", "startup_send_detail", "startup_send_writer_seq", "startup_send_updated_at", "startup_send_pass_id")
			requireCallerExportPrivateFieldsAbsent(t, persisted, privateFieldNames)
			for _, value := range privateValues {
				require.NotContains(t, string(payload), value, "private command content, keys and digests must not leak through any nested retained source path")
			}
			_, repeated, err := store.Page(ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: acquisition.ID}, 100)
			require.NoError(t, err, "repage this same acquisition without rebuilding any live-source projection")
			require.Equal(t, rows, repeated, "the complete scrubbed source must remain the exact frozen public export on subsequent pages")
			var repeatedPayload json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM code_review_export_rows WHERE org_id=$1 AND export_id=$2 AND position=1`, f.org, acquisition.ID).Scan(&repeatedPayload), "read the retained bytes again after paging")
			require.Equal(t, payload, repeatedPayload, "paging must not change the retained privacy-checked payload")
		})
	}
}

func markedCallerExportJSON(t *testing.T, raw, marker string) string {
	t.Helper()
	var object map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &object), "decode the minimal SQL-scope fixture before initial immutable installation")
	object["synthetic_private_marker"] = marker
	encoded, err := json.Marshal(object)
	require.NoError(t, err, "encode a distinct private marker without changing required scope values")
	return string(encoded)
}

func seedCallerExportReviewer(t *testing.T, ctx context.Context, tx pgx.Tx, f callerSendSchemaFixture, assessmentID *uuid.UUID, target, created, digest string, markers []string) {
	t.Helper()
	owner := callerSendSchemaOwners()[0]
	pending := callerSendSchemaShape(owner, f, "pending")
	pending["input"] = markedCallerExportJSON(t, pending["input"].(string), markers[0])
	pending["wake"] = markedCallerExportJSON(t, pending["wake"].(string), markers[1])
	pending["digest"] = digest
	values := map[string]any{
		"id": f.owner, "org_id": f.org, "session_id": f.session, "assessment_id": assessmentID,
		"agent_provider": "codex", "agent_model": "synthetic-export-model", "role": "reviewer", "status": "queued",
		"raw_output": markers[2], "structured_result": fmt.Sprintf(`{"thread_id":%q,"summary":"Synthetic public reviewer output","state":"visible","digest":"public-evidence-digest"}`, f.thread.String()), "created_at": created,
	}
	for _, field := range callerSendSchemaFields(owner) {
		values[owner.prefix+field] = pending[field]
	}
	require.NoError(t, callerSendSchemaInsertMap(ctx, tx, owner.table, values), "install an actual fresh pending reviewer with every immutable private marker already present")
	attempt := callerSendSchemaShape(owner, f, "attempting")
	writer := int32(0)
	for _, state := range []string{"attempting", "unknown", target} {
		next := callerSendSchemaShape(owner, f, state)
		next["attempt_id"] = attempt["attempt_id"]
		var originalDeadline time.Time
		if state == "observed-manual" {
			require.NoError(t, tx.QueryRow(ctx, `SELECT dispatch_observe_deadline FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&originalDeadline), "restore the actual retained deadline before the privacy fixture settles by observation")
			next["observe_deadline"] = originalDeadline
		}
		writer++
		next["writer_seq"] = writer
		fields := []string{"state", "attempt_id", "attempt_seq", "attempted_at", "due_at", "observe_deadline", "attention_at", "observed_at", "message_id", "resolution", "detail", "writer_seq", "updated_at"}
		set, args := make([]string, len(fields)), make([]any, len(fields))
		for i, field := range fields {
			set[i], args[i] = owner.prefix+field+fmt.Sprintf("=$%d", i+4), next[field]
		}
		_, err := callerSendSchemaUpdate(ctx, tx, owner, f, strings.Join(set, ","), args...)
		require.NoError(t, err, "walk pending, attempting, unknown and the requested legal terminal-or-attention state through the actual owner trigger")
		if state == "observed-manual" {
			var retainedDeadline time.Time
			require.NoError(t, tx.QueryRow(ctx, `SELECT dispatch_observe_deadline FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.owner).Scan(&retainedDeadline), "read the committed-in-unit fixture history after its observed acceptance")
			require.Equal(t, originalDeadline.UTC(), retainedDeadline.UTC(), "observed acceptance must preserve the original deadline without weakening any export privacy assertion")
		}
	}
}

func requireCallerExportPrivateFieldsAbsent(t *testing.T, value any, privateNames []string) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for _, name := range privateNames {
			require.NotContains(t, typed, name, "private caller field names must not appear at any retained nested object boundary")
		}
		for _, child := range typed {
			requireCallerExportPrivateFieldsAbsent(t, child, privateNames)
		}
	case []any:
		for _, child := range typed {
			requireCallerExportPrivateFieldsAbsent(t, child, privateNames)
		}
	}
}
