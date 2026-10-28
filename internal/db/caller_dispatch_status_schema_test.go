package db

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func callerDispatchStatusMigrationSQL(t *testing.T, version int, direction string) string {
	t.Helper()
	names := map[int]string{390: "caller_dispatch_status_schema", 391: "caller_dispatch_status_validation", 392: "caller_dispatch_status_readiness"}
	name, ok := names[version]
	require.True(t, ok, "the status prerequisite helper must name one exact appended migration")
	file := filepath.Join("..", "..", "migrations", fmtStatusMigrationName(version, name, direction))
	raw, err := os.ReadFile(file)
	require.NoError(t, err, "the exact appended migration must be readable")
	return string(raw)
}

func fmtStatusMigrationName(version int, name, direction string) string {
	// The three finite versions share this prefix; no arbitrary migration path is
	// accepted by the caller-facing helper above.
	return "000" + strconv.Itoa(version) + "_" + name + "." + direction + ".sql"
}

func TestCallerDispatchStatusSchemaStatic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		version       int
		direction     string
		before, after string
		want          []string
	}{
		{name: "install distinct allowance and disabled prerequisite", version: 390, direction: "up", before: "LOCK TABLE code_review_session_metadata", after: "LOCK TABLE caller_send_intent_control", want: []string{
			"SET LOCAL lock_timeout = '5s'", "caller_send_intents_must_be_disabled_for_status_schema",
			"dispatch_status_ready boolean NOT NULL DEFAULT false", "CHECK (NOT intents_enabled OR (schema_ready AND dispatch_status_ready))",
			"DROP CONSTRAINT code_review_session_metadata_status_code_check", "ADD CONSTRAINT code_review_session_metadata_status_code_v2_check",
			"'review_loop_detected','code_reviewer_dispatch_unresolved')) NOT VALID", "schema_ready AND dispatch_status_ready AND intents_enabled FOR SHARE",
			"retained_dispatch_unresolved_status_prevents_rollback", "unresolved_caller_send_prevents_rollback"}},
		{name: "validation is its own boundary", version: 391, direction: "up", want: []string{"SET LOCAL lock_timeout = '5s'", "VALIDATE CONSTRAINT code_review_session_metadata_status_code_v2_check"}},
		{name: "new readiness positively distinguishes the allowance", version: 392, direction: "up", before: "LOCK TABLE code_review_session_metadata", after: "LOCK TABLE caller_send_intent_control", want: []string{
			"SET LOCAL lock_timeout = '5s'", "conname = 'code_review_session_metadata_status_code_v2_check' AND convalidated",
			"conname = 'code_review_session_metadata_status_code_check'", "caller_send_dispatch_status_enablement_check' AND convalidated",
			"caller_dispatch_status_schema_not_ready", "SET dispatch_status_ready = true"}},
		{name: "highest down refuses before readiness changes", version: 392, direction: "down", before: "SELECT caller143_assert_send_rollback_safe()", after: "UPDATE caller_send_intent_control", want: []string{
			"SET LOCAL lock_timeout = '5s'", "LOCK TABLE code_review_session_metadata, caller_send_intent_control,",
			"IN SHARE ROW EXCLUSIVE MODE", "dispatch_status_ready = false, intents_enabled = false", "forward-fix only"}},
		{name: "lowest down checks before exact validated restoration", version: 390, direction: "down", before: "SELECT caller143_assert_send_rollback_safe()", after: "DROP CONSTRAINT code_review_session_metadata_status_code_v2_check", want: []string{
			"SET LOCAL lock_timeout = '5s'", "LOCK TABLE code_review_session_metadata, caller_send_intent_control,", "IN ACCESS EXCLUSIVE MODE",
			"ADD CONSTRAINT code_review_session_metadata_status_code_check", "DROP COLUMN dispatch_status_ready", "forward fix"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sql := callerDispatchStatusMigrationSQL(t, tt.version, tt.direction)
			for _, fragment := range tt.want {
				require.Contains(t, sql, fragment, "the appended prerequisite must preserve its finite reviewed guard")
			}
			if tt.before != "" {
				first, later := strings.Index(sql, tt.before), strings.Index(sql, tt.after)
				require.GreaterOrEqual(t, first, 0, "the required first lock or guard must actually exist")
				require.Greater(t, later, first, "serialization and refusal must precede dependent locks or destructive DDL")
			}
			require.NotContains(t, sql, "intents_enabled = true", "no appended migration can enable participation")
			if tt.version == 390 && tt.direction == "down" {
				require.NotContains(t, sql, "NOT VALID", "the exact old CHECK must be fully validated on restoration")
				old := callerSendSchemaSQL(t, "000299_code_review_loop_status.up.sql")
				oldDefinition := strings.TrimSpace(old[strings.Index(old, "CHECK ("):])
				require.Contains(t, sql, oldDefinition, "rollback must restore the exact old finite allowance without rewriting rows")
			}
		})
	}
	prepare, err := os.ReadFile("code_review_dispatch_prepare.go")
	require.NoError(t, err, "read the actual preparation SQL independently of its mock expectation constant")
	require.Contains(t, string(prepare), "c.schema_ready AND c.dispatch_status_ready AND c.intents_enabled FOR SHARE OF c", "actual preparation must require the appended readiness interlock, not only old flags")
}
