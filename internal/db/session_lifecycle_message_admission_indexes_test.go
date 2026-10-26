package db

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMessageAdmissionLookupIndexMigrationShape(t *testing.T) {
	t.Parallel()
	tests := []struct{ path, expected string }{
		{"../../migrations/000381_admission_active_repair_lookup.up.sql", "CREATE INDEX CONCURRENTLY idx_admission_active_repair_lookup ON pull_request_repair_runs ((CASE WHEN active THEN org_id END),(CASE WHEN active THEN session_id END),id) WHERE (CASE WHEN active THEN org_id END) IS NOT NULL;"},
		{"../../migrations/000381_admission_active_repair_lookup.down.sql", "DROP INDEX CONCURRENTLY idx_admission_active_repair_lookup;"},
		{"../../migrations/000382_admission_pending_question_lookup.up.sql", "CREATE INDEX CONCURRENTLY idx_admission_pending_question_lookup ON session_questions ((CASE WHEN status='pending' THEN org_id END),(CASE WHEN status='pending' THEN session_id END)) WHERE (CASE WHEN status='pending' THEN org_id END) IS NOT NULL;"},
		{"../../migrations/000382_admission_pending_question_lookup.down.sql", "DROP INDEX CONCURRENTLY idx_admission_pending_question_lookup;"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(tt.path)
			require.NoError(t, err, "read the exact standalone admission lookup migration")
			var sql []string
			for _, line := range strings.Split(string(raw), "\n") {
				if !strings.HasPrefix(strings.TrimSpace(line), "--") {
					sql = append(sql, line)
				}
			}
			actual := strings.Join(strings.Fields(strings.Join(sql, "\n")), " ")
			require.Equal(t, tt.expected, actual, "each build/drop must be the complete exact nonunique single statement with no skipped same-name conflict or extra INCLUDE keys")
		})
	}
}

func TestMessageAdmissionLookupQueriesMatchExpressionKeys(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, actual, expected string }{
		{"active repair has only expression-key scope predicates", messageAdmissionRepairLookupQuery, "SELECT id,pull_request_id FROM pull_request_repair_runs WHERE (CASE WHEN active THEN org_id END)=$1 AND (CASE WHEN active THEN session_id END)=$2 ORDER BY id LIMIT $3"},
		{"pending question expression branch preserves exact human-input scope branch", messageAdmissionPendingAnswerLookupQuery, "SELECT EXISTS(SELECT 1 FROM session_questions WHERE (CASE WHEN status='pending' THEN org_id END)=$1 AND (CASE WHEN status='pending' THEN session_id END)=$2) OR EXISTS(SELECT 1 FROM session_human_input_requests r WHERE r.org_id=$1 AND r.session_id=$2 AND r.status='pending' AND (r.thread_id IS NULL OR r.thread_id=$3 OR NOT EXISTS( SELECT 1 FROM session_threads t WHERE t.org_id=$1 AND t.id=r.thread_id AND t.session_id=$2)))"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, strings.Join(strings.Fields(tt.expected), " "), strings.Join(strings.Fields(tt.actual), " "), "actual production constants must constrain the exact partial expression keys without separate base-column MCV predicates or altered human-input semantics")
		})
	}
}

func TestMessageAdmissionLookupRollbackFloorShape(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../migrations/000383_admission_lookup_rollback_floor.down.sql")
	require.NoError(t, err, "read the highest supported admission-index rollback preflight")
	text := string(raw)
	locks, guard, retained := strings.Index(text, "LOCK TABLE "), strings.Index(text, "SELECT lifecycle143_assert_protocol_rollback_safe();"), strings.Index(text, "DO $$")
	require.True(t, locks >= 0 && locks < guard && guard < retained, "the maintenance preflight must lock before provenance and retained-consumer checks")
	require.Contains(t, text, "ERRCODE='55000'", "retained work must fail with the reviewed prerequisite-state SQLSTATE")
	require.Contains(t, text, "public.pull_request_repair_runs", "the preflight must include the active repair lookup table")
	require.Contains(t, text, "public.session_questions", "the preflight must include the pending question lookup table")
	for _, forbidden := range []string{"DROP INDEX", "DELETE FROM", "UPDATE sessions", "ALTER TABLE"} {
		require.NotContains(t, text, forbidden, "the first down must refuse or observe without removing an index or mutating retained work")
	}
}
