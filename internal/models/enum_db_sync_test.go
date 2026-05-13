package models

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEnumValuesMatchCheckConstraints ensures that the Go enum constants stay
// in sync with the DB CHECK constraints defined in migration 000035. If you
// add a new enum value in Go, you must also update the corresponding CHECK
// constraint in a new migration. This test parses the migration SQL and
// compares the allowed values against the Go constants.
//
// Enum sources:
//   - Go: const blocks in internal/models/*_enums.go, issue_source.go, org_settings.go
//   - DB: CHECK constraints in migrations/000035_check_constraints.up.sql
func TestEnumValuesMatchCheckConstraints(t *testing.T) {
	t.Parallel()
	migrationsDir := "../../migrations"

	// Read all *.up.sql migration files (sorted by name so later migrations
	// override earlier constraint definitions).
	files, err := filepath.Glob(filepath.Join(migrationsDir, "*.up.sql"))
	if err != nil || len(files) == 0 {
		t.Skipf("migration files not found (expected when running outside repo root): %v", err)
	}
	sort.Strings(files)

	// Parse CHECK constraint values from migration SQL.
	// Matches patterns like: CHECK (column IN ('val1', 'val2', ...))
	constraintRE := regexp.MustCompile(`chk_(\w+)\s+CHECK\s*\(\s*(\w+)\s+IN\s*\(([\s\S]*?)\)\s*\)`)
	valueRE := regexp.MustCompile(`'([^']+)'`)

	type dbConstraint struct {
		table  string
		column string
		values []string
	}

	// Use a map so later migrations overwrite earlier definitions of the same constraint.
	constraintMap := map[string]dbConstraint{}
	for _, f := range files {
		sql, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for _, m := range constraintRE.FindAllStringSubmatch(string(sql), -1) {
			valMatches := valueRE.FindAllStringSubmatch(m[3], -1)
			var vals []string
			for _, v := range valMatches {
				vals = append(vals, v[1])
			}
			sort.Strings(vals)
			key := m[1] // constraint name suffix e.g. "projects_status"
			constraintMap[key] = dbConstraint{
				table:  strings.TrimSuffix(key, "_"+m[2]),
				column: m[2],
				values: vals,
			}
		}
	}

	var constraints []dbConstraint
	for _, c := range constraintMap {
		constraints = append(constraints, c)
	}

	// Map of constraint name -> Go enum values. Only enums with typed
	// constants in internal/models are covered.
	goEnumValues := map[string][]string{
		// session_enums.go
		"sessions_status": toStrings(
			SessionStatusPending, SessionStatusRunning, SessionStatusIdle,
			SessionStatusAwaitingInput, SessionStatusNeedsHumanGuidance,
			SessionStatusCompleted, SessionStatusPRCreated, SessionStatusFailed,
			SessionStatusCancelled, SessionStatusSkipped,
		),
		"sessions_sandbox_state": toStrings(
			SandboxStateNone, SandboxStateRunning,
			SandboxStateSnapshotted, SandboxStateDestroyed,
		),
		"sessions_agent_type": toStrings(
			AgentTypeClaudeCode, AgentTypeGeminiCLI,
			AgentTypeCodex, AgentTypeAmp, AgentTypePi,
			AgentTypePMAgent,
		),
		"session_threads_status": toStrings(
			ThreadStatusPending, ThreadStatusRunning, ThreadStatusIdle,
			ThreadStatusAwaitingInput, ThreadStatusCompleted,
			ThreadStatusFailed, ThreadStatusCancelled,
		),
		"session_human_input_requests_request_kind": toStrings(
			HumanInputRequestKindFreeText, HumanInputRequestKindSingleChoice,
			HumanInputRequestKindMultiChoice, HumanInputRequestKindToolApproval,
			HumanInputRequestKindActionChoice,
		),
		"session_human_input_requests_status": toStrings(
			HumanInputRequestStatusPending, HumanInputRequestStatusAnswered,
			HumanInputRequestStatusCancelled, HumanInputRequestStatusExpired,
			HumanInputRequestStatusSuperseded,
		),
		// project_enums.go
		"projects_status": toStrings(
			ProjectStatusDraft, ProjectStatusActive, ProjectStatusCompleted,
		),
		"projects_execution_mode": toStrings(
			ProjectExecModeSequential, ProjectExecModeParallel,
			ProjectExecModeDependencyGraph,
		),
		"projects_schedule_unit": toStrings(
			ScheduleUnitHours, ScheduleUnitDays, ScheduleUnitWeeks,
		),
		"project_tasks_status": toStrings(
			ProjectTaskStatusPending, ProjectTaskStatusBlocked,
			ProjectTaskStatusDelegated, ProjectTaskStatusRunning,
			ProjectTaskStatusCompleted, ProjectTaskStatusFailed,
			ProjectTaskStatusSkipped, ProjectTaskStatusCancelled,
		),
		// integration_enums.go
		"integrations_status": toStrings(
			IntegrationStatusActive, IntegrationStatusInactive,
			IntegrationStatusError,
		),
		"integrations_provider": toStrings(
			IntegrationProviderGitHub, IntegrationProviderSentry,
			IntegrationProviderLinear, IntegrationProviderSlack,
			IntegrationProviderNotion,
		),
		// issue_source.go
		"issues_source": toStrings(
			IssueSourceSentry, IssueSourceLinear,
			IssueSourceManual, IssueSourcePMAgent,
		),
		// pm_enums.go
		"pm_plans_status": toStrings(
			PMPlanStatusExecuting, PMPlanStatusCompleted, PMPlanStatusFailed,
		),
	}

	for _, c := range constraints {
		key := c.table + "_" + c.column
		goVals, ok := goEnumValues[key]
		if !ok {
			// No Go enum mapping for this constraint — skip.
			continue
		}

		sort.Strings(goVals)

		// Check Go values are a subset of DB values (Go shouldn't have values DB rejects).
		dbSet := toSet(c.values)
		for _, v := range goVals {
			if !dbSet[v] {
				t.Errorf("Go enum value %q for %s.%s is not in DB CHECK constraint.\n"+
					"  Go values: %v\n  DB values: %v\n"+
					"  Add a migration to update the CHECK constraint.",
					v, c.table, c.column, goVals, c.values)
			}
		}

		// Check DB values are a subset of Go values (DB shouldn't allow values Go doesn't define).
		goSet := toSet(goVals)
		for _, v := range c.values {
			if !goSet[v] {
				t.Errorf("DB CHECK constraint value %q for %s.%s has no Go constant.\n"+
					"  Go values: %v\n  DB values: %v\n"+
					"  Add a Go constant or remove the DB value.",
					v, c.table, c.column, goVals, c.values)
			}
		}
	}
}

func toStrings[T ~string](vals ...T) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = string(v)
	}
	return out
}

func toSet(vals []string) map[string]bool {
	s := make(map[string]bool, len(vals))
	for _, v := range vals {
		s[v] = true
	}
	return s
}
