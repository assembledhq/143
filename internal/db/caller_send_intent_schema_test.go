package db

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCallerSendIntentSchemaStaticColumns(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		prefix string
		last   string
	}{
		{name: "reviewer dispatch", prefix: "dispatch_", last: "wake jsonb"},
		{name: "initial loop startup", prefix: "startup_send_", last: "pass_id uuid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sql := callerSendSchemaSQL(t, "000384_caller_send_intents.up.sql")
			expected := []string{"version smallint", "key text", "input jsonb", "digest text", "thread_id uuid",
				"state text", "attempt_id uuid", "attempt_seq integer", "attempted_at timestamptz", "due_at timestamptz",
				"observe_deadline timestamptz", "attention_at timestamptz", "observed_at timestamptz", "message_id bigint",
				"resolution text", "detail text", "writer_seq integer", "updated_at timestamptz", tt.last}
			actual := regexp.MustCompile(`(?m)^\s*ADD COLUMN (`+tt.prefix+`\w+ [a-z]+)[,;]$`).FindAllStringSubmatch(sql, -1)
			columns := make([]string, len(actual))
			for i, match := range actual {
				columns[i] = strings.TrimPrefix(match[1], tt.prefix)
			}
			require.Equal(t, expected, columns, "all nineteen private owner columns must retain exact nullable no-default storage")
		})
	}
}

func TestCallerSendIntentSchemaStaticGuards(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		file string
		want []string
	}{
		{name: "truth and digest", file: "000384_caller_send_intents.up.sql", want: []string{
			"octet_length(value) = 64", `value COLLATE "C" ~ '^[0-9a-f]{64}$'`,
			") IS TRUE) NOT VALID", "WHERE singleton AND schema_ready AND intents_enabled FOR SHARE",
			"WHEN 'code-review-reviewer-dispatch' THEN 'cr-dispatch:v1:'", "WHEN 'review-loop-initial-send' THEN 'rl-startup:v1:'",
			"octet_length(NEW.envelope::text) > 8388608", "octet_length(NEW.dispatch_wake::text) > 8388608",
			"BEFORE UPDATE OF dispatch_input,dispatch_wake", "BEFORE UPDATE OF startup_send_input", "BEFORE UPDATE OF envelope,startup_input"}},
		{name: "correct reverse pass key", file: "000387_review_loop_startup_pass_key.up.sql", want: []string{
			"(org_id,session_id,loop_id,id)", "CREATE UNIQUE INDEX CONCURRENTLY"}},
		{name: "deferred full scope reverse binding", file: "000388_caller_send_intent_validation.up.sql", want: []string{
			"FOREIGN KEY (org_id,session_id,id,startup_send_pass_id)",
			"REFERENCES session_review_loop_passes(org_id,session_id,loop_id,id)",
			"DEFERRABLE INITIALLY DEFERRED NOT VALID", "VALIDATE CONSTRAINT fk_srl_startup_pass"}},
		{name: "highest readiness is not enablement", file: "000389_caller_send_intent_readiness.up.sql", want: []string{
			"i.indisvalid AND i.indisready", "c.convalidated", "VALUES (true,true,false,now())",
			"DO UPDATE SET schema_ready = true, updated_at = now()"}},
		{name: "highest rollback first", file: "000389_caller_send_intent_readiness.down.sql", want: []string{
			"caller_send_intent_control, code_review_agent_results, session_review_loops,",
			"session_review_loop_passes, review_loop_start_intents, caller_send_command_stamps,",
			"session_publications, automation_runs IN SHARE ROW EXCLUSIVE MODE",
			"SELECT caller143_assert_send_rollback_safe()", "schema_ready = false, intents_enabled = false"}},
		{name: "lowest rollback rechecks", file: "000384_caller_send_intents.down.sql", want: []string{
			"IN ACCESS EXCLUSIVE MODE", "SELECT caller143_assert_send_rollback_safe()"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sql := callerSendSchemaSQL(t, tt.file)
			for _, fragment := range tt.want {
				require.Contains(t, sql, fragment, "migration must retain the reviewed passive schema guard")
			}
			require.NotContains(t, sql, "intents_enabled = true", "migrations must never enable caller-send participation")
		})
	}
}

func TestCallerSendIntentSchemaStaticStandaloneIndexes(t *testing.T) {
	t.Parallel()
	tests := []struct{ name string }{
		{name: "000385_code_review_dispatch_due"},
		{name: "000386_review_loop_startup_due"},
		{name: "000387_review_loop_startup_pass_key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, direction := range []string{"up", "down"} {
				sql := strings.TrimSpace(callerSendSchemaSQL(t, tt.name+"."+direction+".sql"))
				require.Equal(t, 1, strings.Count(sql, ";"), "each concurrent index migration must be exactly one statement")
				require.True(t, strings.HasSuffix(sql, ";"), "standalone concurrent statement must be terminated")
				require.NotContains(t, strings.ToUpper(sql), "IF NOT EXISTS", "an invalid leftover index must not be silently adopted")
			}
		})
	}
}

func TestCallerSendIntentSchemaStaticPayloadChecksAreInstallOnly(t *testing.T) {
	t.Parallel()
	sql := callerSendSchemaSQL(t, "000384_caller_send_intents.up.sql")
	tests := []struct{ table, constraint string }{
		{table: "code_review_agent_results", constraint: "chk_cra_dispatch_send"},
		{table: "session_review_loops", constraint: "chk_srl_startup_send"},
		{table: "review_loop_start_intents", constraint: "chk_review_loop_start_intent"},
	}
	for _, tt := range tests {
		t.Run(tt.table, func(t *testing.T) {
			t.Parallel()
			prefix := "ALTER TABLE " + tt.table + " ADD CONSTRAINT " + tt.constraint
			start := strings.Index(sql, prefix)
			require.GreaterOrEqual(t, start, 0, "the exact owner/start shape constraint must exist")
			end := strings.Index(sql[start:], ") IS TRUE) NOT VALID;")
			require.GreaterOrEqual(t, end, 0, "the shape constraint must keep its NULL-safe outer truth boundary")
			shape := sql[start : start+end]
			for _, expensive := range []string{"jsonb_typeof", "::text", "caller143_send_input_valid"} {
				// Owner UUID key construction is scalar and does not detoast payload.
				if expensive == "::text" {
					shape = strings.ReplaceAll(shape, "id::text", "id")
				}
				require.NotContains(t, shape, expensive, "ordinary updates must not render or inspect the large immutable retained payload")
			}
		})
	}
}

func callerSendSchemaSQL(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
	require.NoError(t, err, "caller-send migration must be readable")
	return string(contents)
}
