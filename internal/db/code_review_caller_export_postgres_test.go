package db

import (
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// This intentionally small schema isolates the actual export SQL projection.
// It does not establish intent CHECK/trigger validity or migration correctness.
func TestCodeReviewExportPostgresCallerIntentNonDisclosure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		populated bool
	}{
		{name: "legacy row does not expose private nullable columns"},
		{name: "populated intent does not expose command or settlement metadata", populated: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newExportFixture(t)
			f.exec(t, "SET TIME ZONE 'UTC'")
			f.exec(t, `ALTER TABLE code_review_agent_results
				ADD COLUMN agent_provider text, ADD COLUMN agent_model text, ADD COLUMN role text, ADD COLUMN raw_output text,
				ADD COLUMN dispatch_version smallint, ADD COLUMN dispatch_key text, ADD COLUMN dispatch_input jsonb,
				ADD COLUMN dispatch_digest text, ADD COLUMN dispatch_thread_id uuid, ADD COLUMN dispatch_state text, ADD COLUMN dispatch_attempt_id uuid,
				ADD COLUMN dispatch_attempt_seq integer, ADD COLUMN dispatch_attempted_at timestamptz,
				ADD COLUMN dispatch_due_at timestamptz,
				ADD COLUMN dispatch_observe_deadline timestamptz, ADD COLUMN dispatch_observed_at timestamptz,
				ADD COLUMN dispatch_attention_at timestamptz, ADD COLUMN dispatch_message_id bigint,
				ADD COLUMN dispatch_resolution text, ADD COLUMN dispatch_detail text, ADD COLUMN dispatch_writer_seq integer,
				ADD COLUMN dispatch_updated_at timestamptz, ADD COLUMN dispatch_wake jsonb`)
			metadataID, resultID := uuid.New(), uuid.New()
			f.legacy(t, metadataID, "2026-08-01", "2026-08-01", "synthetic-output", 101, nil)
			f.exec(t, `INSERT INTO code_review_agent_results
				(id,org_id,session_id,assessment_id,status,structured_result,created_at,agent_provider,agent_model,role,raw_output)
				VALUES($1,$2,$3,NULL,'queued','{"thread_id":"synthetic-thread"}','2026-08-03T00:00:00Z','codex','synthetic-model','reviewer','synthetic private raw output')`, resultID, f.org, f.session)
			if tt.populated {
				// Every private field is non-NULL so a forgotten exclusion cannot
				// pass merely because a nullable schema default hides its value.
				f.exec(t, `UPDATE code_review_agent_results SET
					dispatch_version=1,dispatch_key='synthetic-private-command-key',dispatch_input='{"message":"synthetic frozen command"}',
					dispatch_digest=repeat('ab',32),dispatch_thread_id=$4,dispatch_state='attention',dispatch_attempt_id=$3,
					dispatch_attempt_seq=1,dispatch_attempted_at='2026-08-03',dispatch_due_at='2026-08-03',
					dispatch_observe_deadline='2026-08-03',dispatch_observed_at='2026-08-03',
					dispatch_attention_at='2026-08-03',dispatch_message_id=7,dispatch_resolution='synthetic-resolution',
					dispatch_detail='synthetic-detail',dispatch_writer_seq=1,dispatch_updated_at='2026-08-03',
					dispatch_wake='{"job_type":"run_code_review","payload":{"synthetic":"private wake"}}'
					WHERE org_id=$1 AND id=$2`, f.org, resultID, uuid.New(), uuid.New())
			}
			store := NewCodeReviewExportStore(f.conn)
			acquisition, err := store.Acquire(f.ctx, f.org, f.filters(models.ReviewExportCreated))
			require.NoError(t, err, "the actual export query must acquire the synthetic retained legacy source")
			_, rows, err := store.Page(f.ctx, f.org, models.CodeReviewExportCursor{AcquisitionID: acquisition.ID}, 100)
			require.NoError(t, err, "read the frozen public export rather than marshal a model that SQL does not use")
			require.Len(t, rows, 1, "the fixture must exercise exactly its intended source payload")
			var actual []map[string]any
			require.NoError(t, json.Unmarshal(rows[0].AgentResults, &actual), "decode actual SQL-generated agent-result objects")
			expected := []map[string]any{{
				"id": resultID.String(), "session_id": f.session.String(), "assessment_id": nil,
				"agent_provider": "codex", "agent_model": "synthetic-model", "role": "reviewer", "status": "queued",
				"structured_result": map[string]any{"thread_id": "synthetic-thread"}, "created_at": "2026-08-03T00:00:00+00:00",
			}}
			require.Equal(t, expected, actual, "the complete pre-intent public field/value allowlist must survive unchanged without org, raw output or any private command/attempt field")
		})
	}
}
