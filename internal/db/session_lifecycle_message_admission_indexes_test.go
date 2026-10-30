package db

import (
	"fmt"
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

func TestMessageAdmissionLookupRollbackContractDrift(t *testing.T) {
	t.Parallel()
	consumer, err := os.ReadFile("../../migrations/000380_lifecycle_consumer_index_rollback_floor.down.sql")
	require.NoError(t, err, "read the retained-consumer rollback preflight used as the admission floor's contract")
	admission, err := os.ReadFile("../../migrations/000383_admission_lookup_rollback_floor.down.sql")
	require.NoError(t, err, "read the actual admission rollback floor rather than a copied SQL fixture")

	replace := func(before, after string) func(string) string {
		return func(sql string) string { return strings.Replace(sql, before, after, 1) }
	}
	const protocol = "SELECT lifecycle143_assert_protocol_rollback_safe();"
	tests := []struct {
		name        string
		mutate      func(string) string
		consumer    bool
		expectedErr string
	}{
		{name: "actual migrations differ only by their intentional retained-work messages"},
		{
			name:   "admission lock-list extension is permitted after the complete consumer prefix",
			mutate: replace("public.session_human_input_requests", "public.session_human_input_requests, public.extra_retained_work"),
		},
		{
			name:        "recovery predicate cannot change",
			mutate:      replace("workspace_recovery_id IS NOT NULL", "workspace_recovery_id IS NULL"),
			expectedErr: "retained-consumer guards differ",
		},
		{
			name:        "bound dispatch evidence cannot disappear",
			mutate:      replace("OR job_id IS NOT NULL OR runtime_id IS NOT NULL", "OR runtime_id IS NOT NULL"),
			expectedErr: "retained-consumer guards differ",
		},
		{
			name:        "retained job status cannot change",
			mutate:      replace("status='awaiting_start_control'", "status='queued'"),
			expectedErr: "retained-consumer guards differ",
		},
		{
			name:        "retained job type cannot change",
			mutate:      replace("job_type='snapshot_object_cleanup'", "job_type='session_execute'"),
			expectedErr: "retained-consumer guards differ",
		},
		{
			name:        "retained evidence table cannot change",
			mutate:      replace("SELECT 1 FROM session_runtime_start_preparations", "SELECT 1 FROM session_threads"),
			expectedErr: "retained-consumer guards differ",
		},
		{
			name:        "consumer-side predicate drift is also rejected",
			mutate:      replace("WHERE admission_state IS NOT NULL", "WHERE admission_state IS NULL"),
			consumer:    true,
			expectedErr: "retained-consumer guards differ",
		},
		{
			name:        "retained refusal SQLSTATE cannot change",
			mutate:      replace("ERRCODE='55000'", "ERRCODE='40001'"),
			expectedErr: "retained-consumer guards differ",
		},
		{
			name:        "guard whitespace is not silently normalized with predicates",
			mutate:      replace("IF EXISTS", "IF  EXISTS"),
			expectedErr: "retained-consumer guards differ",
		},
		{
			name:        "unreviewed exception message cannot match the normalization target",
			mutate:      replace("'retained lifecycle consumers prevent admission index rollback'", "'retained lifecycle consumers prevent rollback'"),
			expectedErr: "admission preflight: expected exactly one intentional retained-work exception literal",
		},
		{
			name:        "protocol function cannot change",
			mutate:      replace(protocol, "SELECT lifecycle143_assert_other_rollback_safe();"),
			expectedErr: "admission preflight: expected one lock, protocol guard, and retained guard",
		},
		{
			name:        "consumer protocol guard cannot disappear",
			mutate:      replace(protocol, ""),
			consumer:    true,
			expectedErr: "consumer preflight: expected one lock, protocol guard, and retained guard",
		},
		{
			name: "protocol guard cannot run before maintenance locks",
			mutate: func(sql string) string {
				sql = strings.Replace(sql, protocol+"\n", "", 1)
				return strings.Replace(sql, "LOCK TABLE ", protocol+"\nLOCK TABLE ", 1)
			},
			expectedErr: "admission preflight: expected locks before protocol before retained guard",
		},
		{
			name: "protocol guard cannot run after retained evidence checks",
			mutate: func(sql string) string {
				sql = strings.Replace(sql, protocol+"\n", "", 1)
				return strings.Replace(sql, "END $$;", "END $$;\n"+protocol, 1)
			},
			expectedErr: "admission preflight: expected locks before protocol before retained guard",
		},
		{
			name:        "duplicate protocol guard cannot mask a reordered guard",
			mutate:      replace(protocol, protocol+"\n"+protocol),
			expectedErr: "admission preflight: expected one lock, protocol guard, and retained guard",
		},
		{
			name:        "lock mode cannot weaken",
			mutate:      replace("IN ACCESS EXCLUSIVE MODE", "IN ACCESS SHARE MODE"),
			expectedErr: "admission preflight: expected one ACCESS EXCLUSIVE lock statement",
		},
		{
			name:        "consumer lock cannot disappear from admission prefix",
			mutate:      replace("public.session_lifecycle_cutovers, ", ""),
			expectedErr: "consumer lock list is not an ordered admission prefix",
		},
		{
			name:        "consumer locks cannot reorder in admission prefix",
			mutate:      replace("public.sessions, public.session_lifecycle_cutovers", "public.session_lifecycle_cutovers, public.sessions"),
			expectedErr: "consumer lock list is not an ordered admission prefix",
		},
		{
			name:        "new lock cannot be inserted inside the consumer prefix",
			mutate:      replace("public.sessions, ", "public.sessions, public.extra_retained_work, "),
			expectedErr: "consumer lock list is not an ordered admission prefix",
		},
		{
			name:        "admission lock list cannot be shorter than consumer prefix",
			mutate:      replace("public.thread_runtimes,\n    public.session_runtime_start_preparations, public.pull_request_repair_runs,\n    public.session_questions, public.session_human_input_requests", "public.thread_runtimes"),
			expectedErr: "consumer lock list is not an ordered admission prefix",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			consumerSQL, admissionSQL := string(consumer), string(admission)
			if tt.mutate != nil {
				original := admissionSQL
				if tt.consumer {
					original = consumerSQL
					consumerSQL = tt.mutate(consumerSQL)
				} else {
					admissionSQL = tt.mutate(admissionSQL)
				}
				actual := admissionSQL
				if tt.consumer {
					actual = consumerSQL
				}
				require.NotEqual(t, original, actual, "each negative control must mutate the selected actual migration")
			}
			err := messageAdmissionLookupRollbackContractError(consumerSQL, admissionSQL)
			if tt.expectedErr != "" {
				require.EqualError(t, err, tt.expectedErr, "the contract check must detect the exact mutation rather than hide semantic drift")
				return
			}
			require.NoError(t, err, "admission rollback must retain the consumer preflight and extend its ordered lock prefix")
		})
	}
}

func messageAdmissionLookupRollbackContractError(consumer, admission string) error {
	consumerGuard, consumerLocks, err := messageAdmissionLookupRollbackParts(consumer)
	if err != nil {
		return fmt.Errorf("consumer preflight: %w", err)
	}
	admissionGuard, admissionLocks, err := messageAdmissionLookupRollbackParts(admission)
	if err != nil {
		return fmt.Errorf("admission preflight: %w", err)
	}
	if consumerGuard != admissionGuard {
		return fmt.Errorf("retained-consumer guards differ")
	}
	if len(admissionLocks) < len(consumerLocks) {
		return fmt.Errorf("consumer lock list is not an ordered admission prefix")
	}
	for i, table := range consumerLocks {
		if admissionLocks[i] != table {
			return fmt.Errorf("consumer lock list is not an ordered admission prefix")
		}
	}
	return nil
}

func messageAdmissionLookupRollbackParts(sql string) (string, []string, error) {
	const protocol = "SELECT lifecycle143_assert_protocol_rollback_safe();"
	if strings.Count(sql, "LOCK TABLE ") != 1 || strings.Count(sql, protocol) != 1 ||
		strings.Count(sql, "DO $$") != 1 || strings.Count(sql, "END $$;") != 1 {
		return "", nil, fmt.Errorf("expected one lock, protocol guard, and retained guard")
	}
	lock, guard, retained := strings.Index(sql, "LOCK TABLE "), strings.Index(sql, protocol), strings.Index(sql, "DO $$")
	lockEnd := strings.Index(sql[lock:], ";") + lock
	retainedEnd := strings.Index(sql, "END $$;") + len("END $$;")
	if lockEnd < lock || lock >= guard || lockEnd >= guard || guard >= retained || retained >= retainedEnd {
		return "", nil, fmt.Errorf("expected locks before protocol before retained guard")
	}
	statement := strings.Join(strings.Fields(sql[lock:lockEnd+1]), " ")
	const mode = " IN ACCESS EXCLUSIVE MODE;"
	if !strings.HasSuffix(statement, mode) {
		return "", nil, fmt.Errorf("expected one ACCESS EXCLUSIVE lock statement")
	}
	tables := strings.Split(strings.TrimSuffix(strings.TrimPrefix(statement, "LOCK TABLE "), mode), ",")
	for i := range tables {
		tables[i] = strings.TrimSpace(tables[i])
	}
	// Only these two intentional exception literals may differ. Preserve every
	// predicate, table, status, SQLSTATE, and even whitespace in the retained guard.
	const consumerMessage = "'retained lifecycle consumers prevent index rollback'"
	const admissionMessage = "'retained lifecycle consumers prevent admission index rollback'"
	body := sql[retained:retainedEnd]
	if strings.Count(body, consumerMessage)+strings.Count(body, admissionMessage) != 1 {
		return "", nil, fmt.Errorf("expected exactly one intentional retained-work exception literal")
	}
	retainedGuard := strings.NewReplacer(
		consumerMessage, "'retained lifecycle consumers prevent rollback'",
		admissionMessage, "'retained lifecycle consumers prevent rollback'",
	).Replace(body)
	return retainedGuard, tables, nil
}
